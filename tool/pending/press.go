package pending

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/cells"
)

// errRefused ends the press transaction without a change; refused then
// says why.
var errRefused = errors.New("pending: press refused")

// spentBy is the used_by of a button spent by a press of its sibling: no
// event pressed it, and no event id has this form.
const spentBy = "spent:sibling"

// statusOf is what a press of a confirmation button decides.
var statusOf = map[string]Status{ActionApprove: StatusApproved, ActionDeny: StatusDenied}

// Press is a press of button buttonID. In one transaction it marks the
// button used, and for a confirmation moves the call from waiting to
// approved or denied and spends the other button, so two presses never
// both run (design section 5.2). It returns:
//   - ErrNotYours (and nothing) for a press by another principal or in
//     another conversation, ErrNotFound for an unknown button;
//   - ErrUsed or ErrExpired, with the button and its call, for a button
//     already used or past its expiry;
//   - the same Press again when by.Event pressed it before (a rerun).
//
// The call's request cell gets the answer.
func (s *Store) Press(ctx context.Context, buttonID string, by By) (Press, error) {
	if by.Event == "" || by.Principal == "" || by.Conv == "" {
		return Press{}, fmt.Errorf("pending: press %s: a press needs a principal, a conversation and an event", buttonID)
	}
	p, err := s.press(ctx, buttonID, by)
	if errors.Is(err, errRefused) {
		p, err = s.refused(ctx, buttonID, by)
	}
	if err != nil {
		return p, err
	}
	if err := s.answerCell(ctx, p, by); err != nil {
		return Press{}, err
	}
	return p, nil
}

// press marks the button used; errRefused when it is not a valid press.
func (s *Store) press(ctx context.Context, buttonID string, by By) (Press, error) {
	var p Press
	err := s.tx(ctx, func(c *sql.Conn) error {
		res, err := c.ExecContext(ctx, `UPDATE pending_buttons SET used = 1, used_by = ?
			WHERE id = ? AND used = 0 AND principal = ? AND conv = ? AND expires_ms > ?`,
			by.Event, buttonID, by.Principal, by.Conv, s.now().UnixMilli())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return errRefused
		}
		if p.Button, _, err = scanButton(c.QueryRowContext(ctx, `SELECT `+buttonColumns+` FROM pending_buttons WHERE id = ?`, buttonID)); err != nil {
			return err
		}
		if p.Button.Pending == "" {
			return nil
		}
		p.Item, err = decideByPress(ctx, c, p.Button)
		return err
	})
	if err != nil && !errors.Is(err, errRefused) {
		return Press{}, fmt.Errorf("pending: press %s: %w", buttonID, err)
	}
	return p, err
}

// decideByPress moves the button's call from waiting to the button's
// decision and spends its other buttons; errRefused when the call no
// longer waits.
func decideByPress(ctx context.Context, c *sql.Conn, b Button) (Item, error) {
	st, ok := statusOf[b.Action]
	if !ok {
		return Item{}, fmt.Errorf("button %s of call %s has action %q", b.ID, b.Pending, b.Action)
	}
	res, err := c.ExecContext(ctx, `UPDATE pending_calls SET status = ? WHERE id = ? AND status = ?`, string(st), b.Pending, string(StatusWaiting))
	if err != nil {
		return Item{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return Item{}, errRefused
	}
	if _, err := c.ExecContext(ctx, `UPDATE pending_buttons SET used = 1, used_by = ? WHERE pending = ? AND used = 0`, spentBy, b.Pending); err != nil {
		return Item{}, err
	}
	return scanItem(c.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM pending_calls WHERE id = ?`, b.Pending))
}

// refused says why a press did not take: the button belongs to someone
// else, was used, or expired. A press by the event that used the button
// is that same press again.
func (s *Store) refused(ctx context.Context, buttonID string, by By) (Press, error) {
	b, usedBy, err := scanButton(s.db.QueryRowContext(ctx, `SELECT `+buttonColumns+` FROM pending_buttons WHERE id = ?`, buttonID))
	if errors.Is(err, sql.ErrNoRows) {
		return Press{}, fmt.Errorf("pending: press %s: %w", buttonID, ErrNotFound)
	}
	if err != nil {
		return Press{}, fmt.Errorf("pending: press %s: %w", buttonID, err)
	}
	if b.Principal != by.Principal || b.Conv != by.Conv {
		return Press{}, fmt.Errorf("pending: press %s: %w", buttonID, ErrNotYours)
	}
	p := Press{Button: b}
	if b.Pending != "" {
		if p.Item, err = s.Get(ctx, b.Pending); err != nil {
			return Press{}, err
		}
	}
	switch {
	case b.Used && usedBy == by.Event:
		return p, nil
	case !b.Used && !b.Expires.After(s.now()):
		// Nobody pressed it in time; its call may be done since, by the
		// expiry reaching the conversation.
		return p, fmt.Errorf("pending: press %s: %w", buttonID, ErrExpired)
	case b.Used || p.Item.Status == StatusApproved || p.Item.Status == StatusDenied || p.Item.Status == StatusDone:
		if p.Answer, err = s.answer(ctx, b.Pending); err != nil {
			return Press{}, err
		}
		return p, fmt.Errorf("pending: press %s: %w", buttonID, ErrUsed)
	default:
		return p, fmt.Errorf("pending: press %s: %w", buttonID, ErrExpired)
	}
}

// answer is the action of the button a press used to answer call id;
// "" for a quick action's button or a call no press answered.
func (s *Store) answer(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	var action string
	err := s.db.QueryRowContext(ctx, `SELECT action FROM pending_buttons WHERE pending = ? AND used = 1 AND used_by <> ?
		ORDER BY rowid LIMIT 1`, id, spentBy).Scan(&action)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("pending: answer of %s: %w", id, err)
	}
	return action, nil
}

// answerCell records the press in the call's request cell. A cell that
// has its answer already (a rerun) or expired is left as it is.
func (s *Store) answerCell(ctx context.Context, p Press, by By) error {
	if p.Item.ID == "" {
		return nil
	}
	_, err := s.cells.Answer(ctx, cellID(p.Item.ID), p.Button.Action == ActionApprove, by.Principal)
	if err != nil && !errors.Is(err, cells.ErrDecided) && !errors.Is(err, cells.ErrNotFound) {
		return fmt.Errorf("pending: answer request %s: %w", p.Item.ID, err)
	}
	return nil
}
