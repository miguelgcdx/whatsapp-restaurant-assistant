// Package cart owns local-fixture drafts and the explicitly configured LOCAL
// pickup lifecycle. A local context alone is not customer authentication, a
// verified sender, or submission authority; pickup requires stored presentation,
// current execution authority and correlated completion evidence.
package cart

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrInput           = errors.New("invalid cart input")
	ErrNotFound        = errors.New("local cart context or resource not found")
	ErrConflict        = errors.New("cart operation conflicts with committed request")
	ErrStale           = errors.New("cart version is stale")
	ErrUnavailable     = errors.New("cart temporarily unavailable")
	ErrStoredData      = errors.New("invalid stored cart data")
	ErrLimit           = errors.New("cart result exceeds resource or arithmetic budget")
	ErrUnavailableItem = errors.New("menu item unavailable")
)

// ID's unexported value and distinct kind prevent interchanging domain IDs.
// Parsing proves canonical syntax only, never authentication or ownership.
type ID[K any] struct{ value string }
type cartKind struct{}
type conversationKind struct{}
type menuKind struct{}
type unitKind struct{}
type operationKind struct{}
type CartID = ID[cartKind]
type ConversationID = ID[conversationKind]
type MenuID = ID[menuKind]
type UnitID = ID[unitKind]
type OperationID = ID[operationKind]

func (id ID[K]) String() string { return id.value }
func (id ID[K]) MarshalText() ([]byte, error) {
	if !validID(id.value) {
		return nil, ErrInput
	}
	return []byte(id.value), nil
}
func (id *ID[K]) UnmarshalText(b []byte) error {
	v, err := parseID[K](string(b))
	if err != nil {
		return err
	}
	*id = v
	return nil
}
func validID(s string) bool {
	var id pgtype.UUID
	if id.Scan(s) != nil || !id.Valid {
		return false
	}
	v, err := id.Value()
	return err == nil && v == s
}
func parseID[K any](s string) (ID[K], error) {
	if !validID(s) {
		return ID[K]{}, ErrInput
	}
	return ID[K]{s}, nil
}
func ParseCartID(s string) (CartID, error)                 { return parseID[cartKind](s) }
func ParseConversationID(s string) (ConversationID, error) { return parseID[conversationKind](s) }
func ParseMenuID(s string) (MenuID, error)                 { return parseID[menuKind](s) }
func ParseUnitID(s string) (UnitID, error)                 { return parseID[unitKind](s) }

// ParseOperationID restores a server-issued local identity retained by the caller.
// No public/model-owned nonce protocol is provided.
func ParseOperationID(s string) (OperationID, error) { return parseID[operationKind](s) }
func newID[K any]() (ID[K], error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ID[K]{}, ErrUnavailable
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b[:])
	return ID[K]{s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]}, nil
}
func NewOperationID() (OperationID, error) { return newID[operationKind]() }

type Version struct{ value int64 }

func ParseVersion(v int64) (Version, error) {
	if v < 0 {
		return Version{}, ErrInput
	}
	return Version{v}, nil
}
func (v Version) Int64() int64                 { return v.value }
func (v Version) MarshalText() ([]byte, error) { return []byte(strconv.FormatInt(v.value, 10)), nil }
func (v *Version) UnmarshalText(b []byte) error {
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != string(b) {
		return ErrInput
	}
	v.value = n
	return nil
}

type LocalContext struct {
	restaurant       string
	conversation     ConversationID
	business, sender string
}

func (c LocalContext) ConversationID() ConversationID { return c.conversation }
func (c LocalContext) valid() bool {
	return validID(c.restaurant) && validID(c.conversation.value) && text(c.business, 256, true) && text(c.sender, 256, true)
}
func text(s string, max int, nonblank bool) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= max && (!nonblank || strings.TrimSpace(s) != "")
}
func currency(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// Action is closed: only validated Add, Remove and SetNote constructors produce it.
type Action interface{ cartAction() }
type addAction struct {
	menu  MenuID
	count int64
}
type removeAction struct{ units []UnitID }
type noteAction struct {
	units []UnitID
	note  string
}

func (addAction) cartAction()    {}
func (removeAction) cartAction() {}
func (noteAction) cartAction()   {}
func Add(menu MenuID, count int64) (Action, error) {
	if !validID(menu.value) || count <= 0 {
		return nil, ErrInput
	}
	return addAction{menu, count}, nil
}
func targets(ids []UnitID) bool {
	if len(ids) == 0 || len(ids) > resultBudget/72 {
		return false
	}
	seen := make(map[UnitID]bool, len(ids))
	for _, id := range ids {
		if !validID(id.value) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func Remove(ids []UnitID) (Action, error) {
	if !targets(ids) {
		return nil, ErrInput
	}
	return removeAction{append([]UnitID(nil), ids...)}, nil
}
func SetNote(ids []UnitID, note string) (Action, error) {
	if !targets(ids) || !text(note, 500, false) {
		return nil, ErrInput
	}
	return noteAction{append([]UnitID(nil), ids...), note}, nil
}

type Unit struct {
	ID         UnitID `json:"id"`
	MenuID     MenuID `json:"menu_id"`
	PriceMinor int64  `json:"price_minor"`
	Currency   string `json:"currency"`
	Exponent   int16  `json:"exponent"`
	Note       string `json:"note"`
}

// Snapshot deliberately has no fulfillment, fee, final total, or payment fields.
type Snapshot struct {
	ID             CartID         `json:"id"`
	ConversationID ConversationID `json:"conversation_id"`
	Version        Version        `json:"version"`
	Currency       string         `json:"currency"`
	Exponent       int16          `json:"exponent"`
	Units          []Unit         `json:"units"`
	Subtotal       int64          `json:"subtotal"`
}
type Result struct {
	OperationID OperationID `json:"operation_id"`
	Cart        Snapshot    `json:"cart"`
}

const resultBudget = 512 * 1024
