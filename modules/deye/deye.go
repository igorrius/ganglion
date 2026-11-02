// Package deye reads a Deye hybrid inverter through the DeyeCloud OpenAPI.
//
// It is the logic of the deye-reader module (spec section 8.3), ported from
// the Python deye.py the current bot runs. It talks to the world only through
// pdk.Host, so the same code runs under `go test` and inside a WASM module.
package deye

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/pdk"
)

// API is the DeyeCloud EU endpoint.
const API = "https://eu1-developer.deyecloud.com/v1.0"

const (
	gridOnVolts    = 170              // grid voltage above this means mains is present
	staleAfter     = 20 * time.Minute // cloud data older than this is a failed read
	tokenMinLeft   = time.Hour        // log in again when the token has less left
	defaultTokenTL = 24 * time.Hour   // used when the login response has no expiry
	tokenInvalid   = "2101019"        // DeyeCloud code for an invalid token
)

// State keys, the same as in deye.py.
const (
	keyToken      = "token"
	keyTokenFP    = "token_fp"
	keyTokenExp   = "token_exp"
	keyAuthFailed = "auth_failed_fp"
)

var (
	// ErrAuth means the credentials are missing or were rejected. Retrying
	// with the same credentials would get the DeyeCloud account locked.
	ErrAuth = errors.New("deye: authentication failed")
	// ErrStale means the cloud returned data older than 20 minutes.
	ErrStale = errors.New("deye: cloud data is stale")
)

// Sample is one reading. Field names match the samples table of deye.py.
type Sample struct {
	TS     int64   `json:"ts"`
	SOC    int     `json:"soc"`
	BattV  float64 `json:"batt_v"`
	BattW  int     `json:"batt_w"` // negative means charging
	BattA  float64 `json:"batt_a"`
	GridV  float64 `json:"grid_v"`
	Freq   float64 `json:"freq"`
	GridW  int     `json:"grid_w"`
	InvW   int     `json:"inv_w"`
	TempDC float64 `json:"temp_dc"`
	TempHS float64 `json:"temp_hs"`
	GridOn int     `json:"grid_on"`
}

// Read returns the latest sample of the inverter with serial number sn.
func Read(h pdk.Host, sn string) (Sample, error) {
	tok, err := token(h)
	if err != nil {
		return Sample{}, err
	}
	dev, err := latest(h, sn, tok)
	if err != nil {
		return Sample{}, err
	}
	ts, err := dev.CollectionTime.Int64()
	if err != nil {
		return Sample{}, fmt.Errorf("deye: bad collectionTime %q", dev.CollectionTime)
	}
	if age := h.Now().Sub(time.Unix(ts, 0)); age > staleAfter {
		return Sample{}, fmt.Errorf("%w (%.0f min old)", ErrStale, age.Minutes())
	}
	return sample(ts, dev.values())
}

// deviceData is one device of the cloud's /device/latest answer.
type deviceData struct {
	CollectionTime json.Number `json:"collectionTime"`
	DataList       []struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	} `json:"dataList"`
}

// values returns the device's readings by key, as text.
func (d deviceData) values() map[string]string {
	out := make(map[string]string, len(d.DataList))
	for _, v := range d.DataList {
		if v.Value != nil {
			out[v.Key] = fmt.Sprint(v.Value)
		}
	}
	return out
}

// latest asks the cloud for the inverter's latest data. A rejected token
// is forgotten, so the next run logs in again.
func latest(h pdk.Host, sn, tok string) (deviceData, error) {
	var r struct {
		Success bool            `json:"success"`
		Code    json.RawMessage `json:"code"`
		Msg     string          `json:"msg"`
		Devices []deviceData    `json:"deviceDataList"`
	}
	if err := post(h, "/device/latest", map[string]any{"deviceList": []string{sn}}, tok, &r); err != nil {
		return deviceData{}, err
	}
	if !r.Success {
		if code(r.Code) == tokenInvalid {
			h.StatePut(keyToken, "")
		}
		return deviceData{}, fmt.Errorf("deye: cloud read failed: %s %s", code(r.Code), r.Msg)
	}
	if len(r.Devices) == 0 {
		return deviceData{}, errors.New("deye: cloud returned no device data")
	}
	return r.Devices[0], nil
}

// sample parses the readings taken at ts.
func sample(ts int64, values map[string]string) (Sample, error) {
	p := parser{values: values}
	s := Sample{
		TS:     ts,
		SOC:    p.int("SOC"),
		BattV:  p.float("BatteryVoltage"),
		BattW:  p.int("BatteryPower"),
		BattA:  p.float("BatteryCurrent"),
		GridV:  p.float("GridVoltageL1L2"),
		Freq:   p.float("GridFrequency"),
		GridW:  p.int("TotalGridPower"),
		InvW:   p.int("InverterOutputPowerL1L2"),
		TempDC: p.float("DC Temperature"),
		TempHS: p.float("AC Temperature"),
	}
	if p.err != nil {
		return Sample{}, p.err
	}
	if s.GridV > gridOnVolts {
		s.GridOn = 1
	}
	return s, nil
}

// token returns a cached access token or logs in. A rejected login is
// remembered per credential set, so a wrong password costs one attempt.
func token(h pdk.Host) (string, error) {
	creds := map[string]string{}
	for _, k := range []string{"DEYE_APP_ID", "DEYE_APP_SECRET", "DEYE_EMAIL", "DEYE_PASSWORD"} {
		v, ok := h.Secret(k)
		if !ok {
			return "", fmt.Errorf("%w: %s is not set", ErrAuth, k)
		}
		creds[k] = v
	}
	fp := fingerprint(creds["DEYE_EMAIL"], creds["DEYE_PASSWORD"])
	if failed, _ := h.StateGet(keyAuthFailed); failed == fp {
		return "", fmt.Errorf("%w: DeyeCloud rejected these credentials earlier", ErrAuth)
	}
	tok, _ := h.StateGet(keyToken)
	tokFP, _ := h.StateGet(keyTokenFP)
	expRaw, _ := h.StateGet(keyTokenExp)
	if exp, err := strconv.ParseInt(expRaw, 10, 64); err == nil && tok != "" && tokFP == fp &&
		time.Unix(exp, 0).Sub(h.Now()) > tokenMinLeft {
		return tok, nil
	}

	pw := sha256.Sum256([]byte(creds["DEYE_PASSWORD"]))
	var r struct {
		AccessToken string          `json:"accessToken"`
		ExpiresIn   json.Number     `json:"expiresIn"`
		Code        json.RawMessage `json:"code"`
		Msg         string          `json:"msg"`
	}
	body := map[string]string{
		"appSecret": creds["DEYE_APP_SECRET"],
		"email":     creds["DEYE_EMAIL"],
		"password":  hex.EncodeToString(pw[:]),
	}
	if err := post(h, "/account/token?appId="+creds["DEYE_APP_ID"], body, "", &r); err != nil {
		return "", err
	}
	if r.AccessToken == "" {
		h.StatePut(keyAuthFailed, fp)
		return "", fmt.Errorf("%w: DeyeCloud login failed: %s %s", ErrAuth, code(r.Code), r.Msg)
	}
	ttl := defaultTokenTL
	if secs, err := r.ExpiresIn.Float64(); err == nil && secs > 0 {
		ttl = time.Duration(secs) * time.Second
	}
	h.StatePut(keyToken, r.AccessToken)
	h.StatePut(keyTokenFP, fp)
	h.StatePut(keyTokenExp, strconv.FormatInt(h.Now().Add(ttl).Unix(), 10))
	return r.AccessToken, nil
}

func post(h pdk.Host, path string, body any, tok string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req := pdk.Request{
		Method:  "POST",
		URL:     API + path,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    b,
	}
	if tok != "" {
		req.Headers["Authorization"] = "bearer " + tok
	}
	resp, err := h.Fetch(req)
	if err != nil {
		return fmt.Errorf("deye: cloud request %s failed: %w", path, err)
	}
	if resp.Status < 200 || resp.Status > 299 {
		return fmt.Errorf("deye: cloud request %s failed: HTTP %d", path, resp.Status)
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("deye: cloud request %s: bad JSON: %w", path, err)
	}
	return nil
}

func fingerprint(email, password string) string {
	sum := sha256.Sum256([]byte(email + "\x00" + password))
	return hex.EncodeToString(sum[:])[:16]
}

// code renders DeyeCloud's code field, which may be a string or a number.
func code(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// parser reads numeric fields and keeps the first error.
type parser struct {
	values map[string]string
	err    error
}

func (p *parser) float(key string) float64 {
	if p.err != nil {
		return 0
	}
	v, ok := p.values[key]
	if !ok || v == "" {
		p.err = fmt.Errorf("deye: cloud data lacks %s", key)
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		p.err = fmt.Errorf("deye: bad %s value %q", key, v)
	}
	return f
}

func (p *parser) int(key string) int {
	return int(p.float(key))
}
