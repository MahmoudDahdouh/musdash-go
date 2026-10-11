package deploy

import "testing"

func TestRefusedHint(t *testing.T) {
	for said, hinted := range map[string]bool{
		"Error response from daemon: pull access denied for acme/shop, repository does not exist or may require 'docker login'":     true,
		"Error response from daemon: error from registry: denied\ndenied":                                                           true,
		`failed to resolve reference "localhost:5000/private/nginx:1": authorization failed: no basic auth credentials`:             true,
		"Head \"https://ghcr.io/v2/acme/shop/manifests/1\": unauthorized":                                                           true,
		"Error response from daemon: Get \"https://registry.example.com/v2/\": dial tcp: lookup registry.example.com: no such host": false,
		"write /var/lib/docker/tmp/x: no space left on device":                                                                      false,
		"": false,
	} {
		if got := refusedHint(said) != ""; got != hinted {
			t.Errorf("refusedHint(%q): hinted %v, want %v", said, got, hinted)
		}
	}
}
