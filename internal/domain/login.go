package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// LoginID names one of a member's logins of an agent: DefaultLogin for the
// login in the member's home, or the "lgn-" id of an added one. A run
// records the empty id for the default login.
type LoginID string

// DefaultLogin is the id of the login in the member's home.
const DefaultLogin LoginID = "default"

// Added reports whether id names an added login rather than the default.
func (id LoginID) Added() bool { return id != "" && id != DefaultLogin }

// Account is what a session spends: a member's login. An empty Login is the
// member's default login. Its text form is the member id, followed for an
// added login by "/" and the login id; a member id never holds "/", so the
// text of a default login is the member id alone, as before logins existed.
type Account struct {
	Member MemberID
	Login  LoginID
}

// AccountOf is member's default login.
func AccountOf(member MemberID) Account { return Account{Member: member} }

// Default is a with its login dropped: the member's default login.
func (a Account) Default() Account { return Account{Member: a.Member} }

func (a Account) String() string {
	if !a.Login.Added() {
		return string(a.Member)
	}
	return string(a.Member) + "/" + string(a.Login)
}

func (a Account) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

func (a *Account) UnmarshalText(text []byte) error {
	member, login, _ := strings.Cut(string(text), "/")
	a.Member, a.Login = MemberID(member), LoginID(login)
	if !a.Login.Added() {
		a.Login = ""
	}
	return nil
}

// AgentLogin is one of a member's logins of an agent, in the order Cloudfield
// tries them.
type AgentLogin struct {
	MemberID  MemberID
	Agent     string
	ID        LoginID
	Name      string
	Position  int
	Automatic bool
	// Shared is whether the member's account shares reach the login.
	Shared    bool
	CreatedAt time.Time
}

// MaxLoginNameLen bounds a login's name.
const MaxLoginNameLen = 32

// ValidateLoginName checks a name a member gives an added login.
func ValidateLoginName(name string) error {
	if name == "" || len(name) > MaxLoginNameLen {
		return fmt.Errorf("a login name has 1 to %d characters", MaxLoginNameLen)
	}
	if name == string(DefaultLogin) {
		return errors.New(`"default" names the login in your home; choose another name`)
	}
	for i, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return fmt.Errorf("login name %q: use lowercase letters, digits and '-', starting with a letter or digit", name)
		}
	}
	return nil
}
