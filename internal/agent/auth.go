package agent

import (
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// Auth is an agent's login state.
type Auth struct {
	Known    bool      // false: prowl can't tell (a command agent)
	LoggedIn bool      // meaningful when Known
	Account  string    // who, as the agent reports it
	Expires  time.Time // zero when the agent doesn't say
}

// RenewWithin is how early prowl starts asking you to log in again.
const RenewWithin = 72 * time.Hour

// ExpiresSoon is true when a login runs out within d.
func (a Auth) ExpiresSoon(d time.Duration) bool {
	return a.LoggedIn && !a.Expires.IsZero() && time.Until(a.Expires) < d
}

// Auth reads `claude auth status --json`. Versions without JSON output fail
// the command when logged out, which is all prowl can tell from them.
func (claudeBackend) Auth() Auth {
	cmd := exec.Command("claude", "auth", "status", "--json")
	cmd.Env = CleanEnv()
	out, err := cmd.Output()
	var st struct {
		LoggedIn  bool   `json:"loggedIn"`
		Method    string `json:"authMethod"`
		OrgName   string `json:"orgName"`
		Email     string `json:"email"`
		ExpiresAt string `json:"loginExpiresAt"`
	}
	if json.Unmarshal(out, &st) != nil {
		return Auth{Known: err != nil, LoggedIn: false}
	}
	a := Auth{Known: true, LoggedIn: st.LoggedIn, Account: st.Email}
	if st.OrgName != "" {
		a.Account += " (" + st.OrgName + ")"
	}
	if t, err := time.Parse(time.RFC3339, st.ExpiresAt); err == nil {
		a.Expires = t
	}
	return a
}

func (claudeBackend) Login() *exec.Cmd {
	cmd := exec.Command("claude", "auth", "login")
	cmd.Env = CleanEnv()
	return cmd
}

func (codexBackend) Auth() Auth {
	out, err := exec.Command("codex", "login", "status").CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if i := strings.LastIndex(msg, "\n"); i >= 0 {
		msg = strings.TrimSpace(msg[i+1:])
	}
	if err != nil {
		return Auth{Known: true}
	}
	return Auth{Known: true, LoggedIn: true, Account: strings.TrimPrefix(msg, "Logged in using ")}
}

func (codexBackend) Login() *exec.Cmd {
	cmd := exec.Command("codex", "login")
	cmd.Env = CleanEnv()
	return cmd
}

// Auth runs status_command: exit 0 means logged in. It may also print
// {"loggedIn": bool, "account": "...", "expiresAt": "<RFC 3339>"}.
func (b commandBackend) Auth() Auth {
	if strings.TrimSpace(b.status) == "" {
		return Auth{}
	}
	cmd := shell(b.status)
	cmd.Env = CleanEnv()
	out, err := cmd.Output()
	a := Auth{Known: true, LoggedIn: err == nil}
	var st struct {
		LoggedIn  *bool  `json:"loggedIn"`
		Account   string `json:"account"`
		ExpiresAt string `json:"expiresAt"`
	}
	if json.Unmarshal(out, &st) == nil {
		if st.LoggedIn != nil {
			a.LoggedIn = *st.LoggedIn
		}
		a.Account = st.Account
		a.Expires, _ = time.Parse(time.RFC3339, st.ExpiresAt)
	}
	return a
}

func (b commandBackend) Login() *exec.Cmd {
	if strings.TrimSpace(b.login) == "" {
		return nil
	}
	cmd := shell(b.login)
	cmd.Env = CleanEnv()
	return cmd
}
