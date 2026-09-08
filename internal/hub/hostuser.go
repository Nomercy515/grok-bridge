package hub

import (
	"os"
	"os/user"
	"strings"
)

// HostUserLabel is the signed-in host account shown as "Name's sessions list".
// GROK_BRIDGE_USER_NAME overrides; otherwise the GECOS first name, then the login.
func HostUserLabel() string {
	if v := strings.TrimSpace(os.Getenv("GROK_BRIDGE_USER_NAME")); v != "" {
		return v
	}
	u, err := user.Current()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(u.Name)
	login := stripLogin(u.Username)
	if name != "" && !strings.EqualFold(name, u.Username) && !strings.EqualFold(name, login) {
		if fields := strings.Fields(name); len(fields) > 0 && fields[0] != "" {
			return fields[0]
		}
	}
	return login
}

func stripLogin(username string) string {
	username = strings.TrimSpace(username)
	if i := strings.LastIndex(username, `\`); i >= 0 {
		username = username[i+1:]
	}
	if i := strings.Index(username, "@"); i > 0 {
		username = username[:i]
	}
	return username
}
