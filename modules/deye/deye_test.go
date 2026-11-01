package deye

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/pdk"
)

const testSN = "2400000001"

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// fakeHost plays both the runner and DeyeCloud.
type fakeHost struct {
	secrets  map[string]string
	state    map[string]string
	login    func(body map[string]string) string // returns the /account/token response
	latest   func(auth string) string            // returns the /device/latest response
	requests []pdk.Request
}

func newHost() *fakeHost {
	return &fakeHost{
		secrets: map[string]string{
			"DEYE_APP_ID": "app-1", "DEYE_APP_SECRET": "s3cret",
			"DEYE_EMAIL": "me@example.com", "DEYE_PASSWORD": "pw",
		},
		state: map[string]string{},
		login: func(map[string]string) string {
			return `{"code":"1000000","accessToken":"tok-1","expiresIn":"5183999"}`
		},
		latest: func(string) string { return latestJSON(now.Add(-3*time.Minute), "57", "230.4") },
	}
}

func (h *fakeHost) Fetch(req pdk.Request) (pdk.Response, error) {
	h.requests = append(h.requests, req)
	switch {
	case strings.HasPrefix(req.URL, API+"/account/token?appId="):
		var body map[string]string
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return pdk.Response{}, err
		}
		return pdk.Response{Status: 200, Body: []byte(h.login(body))}, nil
	case req.URL == API+"/device/latest":
		return pdk.Response{Status: 200, Body: []byte(h.latest(req.Headers["Authorization"]))}, nil
	}
	return pdk.Response{}, fmt.Errorf("unexpected request %s", req.URL)
}

func (h *fakeHost) StateGet(k string) (string, bool) { v, ok := h.state[k]; return v, ok }
func (h *fakeHost) StatePut(k, v string)             { h.state[k] = v }
func (h *fakeHost) Secret(n string) (string, bool)   { v, ok := h.secrets[n]; return v, ok && v != "" }
func (h *fakeHost) Config(string) (string, bool)     { return "", false }
func (h *fakeHost) Now() time.Time                   { return now }
func (h *fakeHost) Log(string)                       {}

func (h *fakeHost) count(path string) int {
	n := 0
	for _, r := range h.requests {
		if strings.Contains(r.URL, path) {
			n++
		}
	}
	return n
}

// latestJSON builds a /device/latest response in DeyeCloud's shape: every
// value is a string inside dataList.
func latestJSON(collected time.Time, soc, gridV string) string {
	data := map[string]string{
		"SOC": soc, "BatteryVoltage": "52.31", "BatteryPower": "-1250", "BatteryCurrent": "-23.9",
		"GridVoltageL1L2": gridV, "GridFrequency": "50.01", "TotalGridPower": "1830",
		"InverterOutputPowerL1L2": "540", "DC Temperature": "31.5", "AC Temperature": "38.2",
	}
	var list []string
	for k, v := range data {
		list = append(list, fmt.Sprintf(`{"key":%q,"value":%q,"unit":""}`, k, v))
	}
	return fmt.Sprintf(`{"code":"1000000","success":true,"deviceDataList":[{"deviceSn":"%s","collectionTime":%d,"dataList":[%s]}]}`,
		testSN, collected.Unix(), strings.Join(list, ","))
}

func TestReadLogsInAndParsesSample(t *testing.T) {
	h := newHost()
	var loginBody map[string]string
	h.login = func(b map[string]string) string {
		loginBody = b
		return `{"code":"1000000","accessToken":"tok-1","expiresIn":"5183999"}`
	}
	var gotAuth string
	h.latest = func(auth string) string { gotAuth = auth; return latestJSON(now.Add(-3*time.Minute), "57", "230.4") }

	s, err := Read(h, testSN)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	pw := sha256.Sum256([]byte("pw"))
	if loginBody["password"] != hex.EncodeToString(pw[:]) || loginBody["appSecret"] != "s3cret" || loginBody["email"] != "me@example.com" {
		t.Errorf("login body = %v, want sha256 of the password, the app secret and the email", loginBody)
	}
	if !strings.HasSuffix(h.requests[0].URL, "appId=app-1") {
		t.Errorf("login URL = %s, want appId=app-1", h.requests[0].URL)
	}
	if gotAuth != "bearer tok-1" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "bearer tok-1")
	}
	want := Sample{
		TS: now.Add(-3 * time.Minute).Unix(), SOC: 57, BattV: 52.31, BattW: -1250, BattA: -23.9,
		GridV: 230.4, Freq: 50.01, GridW: 1830, InvW: 540, TempDC: 31.5, TempHS: 38.2, GridOn: 1,
	}
	if s != want {
		t.Errorf("sample = %+v\nwant     %+v", s, want)
	}
}

func TestReadReportsGridOffAtLowVoltage(t *testing.T) {
	h := newHost()
	h.latest = func(string) string { return latestJSON(now.Add(-time.Minute), "41", "0.0") }
	s, err := Read(h, testSN)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.GridOn != 0 {
		t.Errorf("GridOn = %d at 0 V, want 0", s.GridOn)
	}
}

func TestReadReusesCachedToken(t *testing.T) {
	h := newHost()
	if _, err := Read(h, testSN); err != nil {
		t.Fatalf("first Read: %v", err)
	}
	if _, err := Read(h, testSN); err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if n := h.count("/account/token"); n != 1 {
		t.Errorf("logged in %d times, want 1", n)
	}
}

func TestReadLogsInAgainWhenTokenExpiresWithinAnHour(t *testing.T) {
	h := newHost()
	h.login = func(map[string]string) string { return `{"code":"1000000","accessToken":"tok-1","expiresIn":"3000"}` }
	if _, err := Read(h, testSN); err != nil {
		t.Fatalf("first Read: %v", err)
	}
	if _, err := Read(h, testSN); err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if n := h.count("/account/token"); n != 2 {
		t.Errorf("logged in %d times, want 2", n)
	}
}

func TestReadLogsInAgainWhenCredentialsChange(t *testing.T) {
	h := newHost()
	if _, err := Read(h, testSN); err != nil {
		t.Fatalf("first Read: %v", err)
	}
	h.secrets["DEYE_PASSWORD"] = "new-pw"
	if _, err := Read(h, testSN); err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if n := h.count("/account/token"); n != 2 {
		t.Errorf("logged in %d times, want 2", n)
	}
}

func TestRejectedLoginIsNotRetriedWithTheSameCredentials(t *testing.T) {
	h := newHost()
	h.login = func(map[string]string) string { return `{"code":"2101006","msg":"password error"}` }

	if _, err := Read(h, testSN); !errors.Is(err, ErrAuth) {
		t.Fatalf("first Read error = %v, want ErrAuth", err)
	}
	if _, err := Read(h, testSN); !errors.Is(err, ErrAuth) {
		t.Fatalf("second Read error = %v, want ErrAuth", err)
	}
	if n := h.count("/account/token"); n != 1 {
		t.Errorf("tried to log in %d times, want 1: DeyeCloud locks the account after a few failures", n)
	}

	h.secrets["DEYE_PASSWORD"] = "fixed"
	h.login = func(map[string]string) string { return `{"code":"1000000","accessToken":"tok-2","expiresIn":"86400"}` }
	if _, err := Read(h, testSN); err != nil {
		t.Fatalf("Read after fixing the password: %v", err)
	}
}

func TestMissingSecretIsAnAuthErrorWithoutNetwork(t *testing.T) {
	h := newHost()
	h.secrets["DEYE_APP_SECRET"] = ""
	if _, err := Read(h, testSN); !errors.Is(err, ErrAuth) {
		t.Fatalf("Read error = %v, want ErrAuth", err)
	}
	if len(h.requests) != 0 {
		t.Errorf("made %d requests, want none", len(h.requests))
	}
}

func TestInvalidTokenIsDroppedForTheNextRead(t *testing.T) {
	h := newHost()
	h.latest = func(string) string { return `{"code":"2101019","msg":"token invalid","success":false}` }
	if _, err := Read(h, testSN); err == nil || errors.Is(err, ErrAuth) {
		t.Fatalf("Read error = %v, want a read error that is not ErrAuth", err)
	}
	h.latest = func(string) string { return latestJSON(now, "60", "231") }
	if _, err := Read(h, testSN); err != nil {
		t.Fatalf("Read after token reset: %v", err)
	}
	if n := h.count("/account/token"); n != 2 {
		t.Errorf("logged in %d times, want 2", n)
	}
}

func TestStaleDataIsAnError(t *testing.T) {
	h := newHost()
	h.latest = func(string) string { return latestJSON(now.Add(-21*time.Minute), "57", "230") }
	if _, err := Read(h, testSN); !errors.Is(err, ErrStale) {
		t.Fatalf("Read error = %v, want ErrStale", err)
	}
}

func TestMissingFieldIsAnError(t *testing.T) {
	h := newHost()
	h.latest = func(string) string {
		return strings.Replace(latestJSON(now, "57", "230"), `"key":"SOC"`, `"key":"SOC_renamed"`, 1)
	}
	_, err := Read(h, testSN)
	if err == nil || !strings.Contains(err.Error(), "SOC") {
		t.Fatalf("Read error = %v, want an error naming SOC", err)
	}
}

func TestHTTPErrorStatusIsAnError(t *testing.T) {
	h := &statusHost{fakeHost: newHost(), status: 502}
	if _, err := Read(h, testSN); err == nil {
		t.Fatal("Read with HTTP 502 succeeded, want error")
	}
}

type statusHost struct {
	*fakeHost
	status int
}

func (h *statusHost) Fetch(pdk.Request) (pdk.Response, error) {
	return pdk.Response{Status: h.status, Body: []byte("bad gateway")}, nil
}

type configHost struct {
	*fakeHost
	config map[string]string
}

func (h *configHost) Config(k string) (string, bool) { v, ok := h.config[k]; return v, ok }

func TestHandleReturnsTheSample(t *testing.T) {
	h := &configHost{fakeHost: newHost(), config: map[string]string{"inverter_sn": testSN}}
	out := pdk.Encode(Handle(h, pdk.Input{Trigger: "cron"}))
	if !out.OK {
		t.Fatalf("output = %+v, want ok", out)
	}
	var s Sample
	if err := json.Unmarshal(out.Result, &s); err != nil || s.SOC != 57 {
		t.Fatalf("result = %s (%v), want a sample with SOC 57", out.Result, err)
	}
}

func TestHandleReportsAuthFailures(t *testing.T) {
	h := &configHost{fakeHost: newHost(), config: map[string]string{"inverter_sn": testSN}}
	h.secrets["DEYE_PASSWORD"] = ""
	if out := pdk.Encode(Handle(h, pdk.Input{Trigger: "cron"})); out.Kind != pdk.KindAuthFailed {
		t.Fatalf("output = %+v, want kind auth_failed", out)
	}
}

func TestHandleNeedsTheSerialNumber(t *testing.T) {
	h := &configHost{fakeHost: newHost()}
	out := pdk.Encode(Handle(h, pdk.Input{Trigger: "cron"}))
	if out.OK || out.Kind != pdk.KindException || !strings.Contains(out.Error, "inverter_sn") {
		t.Fatalf("output = %+v, want an exception naming inverter_sn", out)
	}
	if len(h.requests) != 0 {
		t.Errorf("made %d requests without a serial number", len(h.requests))
	}
}
