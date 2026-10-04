package source

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"

	"golang.org/x/crypto/ssh"
)

// GenerateDeployKey creates an ed25519 key pair. The public half is in the
// one-line authorized_keys form a person pastes into their Git host; the
// private half is an OpenSSH PEM, stored sealed.
func GenerateDeployKey(comment string) (public string, privatePEM []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", nil, err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		line += " " + comment
	}
	return line, pem.EncodeToMemory(block), nil
}
