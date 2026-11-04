package mimiry

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// domain is appended to a bare subdomain: "platform" means https://platform.mimiry.com.
const domain = "mimiry.com"

// ErrNoHost is returned when no host was given. There is deliberately no
// built-in default: a default that names an instance which is not live, or
// one that was retired, sent every bare call of an earlier client to a host
// with no DNS — twice.
var ErrNoHost = errors.New("mimiry: no host configured (set Options.Host or MIMIRY_HOST to a subdomain, hostname or URL, e.g. \"platform\")")

// deploymentID matches a deployment environment identifier such as
// "prod-eu1". It is never a host, and turning it into one is how an
// earlier client reached the wrong instance.
// ErrInvalidHost wraps every refusal of a host value that is not a host.
var ErrInvalidHost = errors.New("mimiry: invalid host")

var deploymentID = regexp.MustCompile(`^(dev|staging|prod)-`)

var subdomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ResolveBaseURL turns a host as a person writes it into the API base URL.
//
//	"platform"                -> https://platform.mimiry.com
//	"api.example.com"         -> https://api.example.com
//	"http://localhost:8080/"  -> http://localhost:8080
//
// A subdomain names a purpose, not a deployment instance: the instance behind a
// subdomain changes while the subdomain stays. A value shaped like an instance
// identifier ("prod-eu1") is refused rather than guessed at.
func ResolveBaseURL(host string) (string, error) {
	h := strings.TrimSpace(host)
	if h == "" {
		return "", ErrNoHost
	}

	if strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") {
		u, err := url.Parse(h)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("%w: %q is not a valid URL", ErrInvalidHost, host)
		}
		return strings.TrimRight(h, "/"), nil
	}

	if strings.ContainsAny(h, "/ ?#@") {
		return "", fmt.Errorf("%w: %q is neither a subdomain, a hostname nor a URL", ErrInvalidHost, host)
	}

	// A dot means it is already a hostname. Checked before the identifier
	// refusal, so a real hostname is never refused for its prefix.
	if strings.Contains(h, ".") {
		return "https://" + strings.TrimRight(h, "."), nil
	}

	if deploymentID.MatchString(h) {
		return "", fmt.Errorf("%w: %q looks like a deployment identifier, not a host; "+
			"pass the subdomain or hostname it answers at (e.g. \"platform\")", ErrInvalidHost, host)
	}
	if !subdomain.MatchString(h) {
		return "", fmt.Errorf("%w: %q is not a valid subdomain", ErrInvalidHost, host)
	}
	return "https://" + h + "." + domain, nil
}
