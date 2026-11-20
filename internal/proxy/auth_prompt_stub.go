//go:build !windows

package proxy

import "errors"

func promptWindowsUpstreamCredentials(string) (promptedUpstreamCredential, error) {
	return promptedUpstreamCredential{}, errors.New("interactive Windows credential prompt is only available on Windows")
}
