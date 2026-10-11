package deploy

import "strings"

// refusals are the words a registry, or Docker on its behalf, refuses a
// pull with when it wants to know who is asking. The same words are the
// answer for an image that does not exist: a registry does not say which.
var refusals = []string{
	"pull access denied", "may require authorization", "requires authorization",
	"no basic auth credentials", "authentication required", "unauthorized", "denied",
}

// signInHint is what a pull that was refused is told to do about it.
// musdash keeps no registry passwords: a private image is pulled with the
// sign-in of the account that runs Docker's commands on the server, which
// is where Docker itself looks.
const signInHint = "If the image is private, sign in to its registry on the server with docker login, as the account musdash runs as there, and deploy again. If it is not, check its name and tag."

// refusedHint is signInHint on a line of its own when what Docker said
// about a failed pull is a refusal, and "" otherwise: a full disk or a
// network that is down are not helped by signing in.
func refusedHint(said string) string {
	said = strings.ToLower(said)
	for _, words := range refusals {
		if strings.Contains(said, words) {
			return "\n" + signInHint
		}
	}
	return ""
}
