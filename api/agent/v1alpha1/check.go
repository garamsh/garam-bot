package v1alpha1

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
)

var (
	sessionPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	taskPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// CheckSession returns the first rule of SES-1 that the session identifier id or spec breaks, nil
// when neither breaks one.
func CheckSession(id string, spec SessionSpec) error {
	if !sessionPattern.MatchString(id) {
		return fmt.Errorf("session %q is not a DNS label", id)
	}
	if spec.Bot.Name == "" {
		return errors.New("bot.name is empty")
	}
	if err := checkEndpoint("model", spec.Model.BaseURL, spec.Model.TokenFile); err != nil {
		return err
	}
	names := map[string]bool{}
	for _, t := range spec.Tools {
		if t.Name == "" || names[t.Name] {
			return fmt.Errorf("tool name %q is empty or repeated", t.Name)
		}
		names[t.Name] = true
		if err := checkEndpoint("tool "+t.Name, t.URL, t.TokenFile); err != nil {
			return err
		}
	}
	if spec.WorkingDir != "" && !absolute(spec.WorkingDir) {
		return fmt.Errorf("workingDir %q is not an absolute, clean path", spec.WorkingDir)
	}

	return nil
}

// Check returns the first rule of TASK-1 the request breaks, nil when it breaks none.
func (t TaskRequest) Check() error {
	if !taskPattern.MatchString(t.ID) {
		return fmt.Errorf("task id %q is not 1 to 128 of [A-Za-z0-9._-]", t.ID)
	}
	if len(t.Input) == 0 {
		return errors.New("input is empty")
	}

	return nil
}

func checkEndpoint(what, rawURL, tokenFile string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%s URL %q is not an absolute http or https URL", what, rawURL)
	}
	if !absolute(tokenFile) {
		return fmt.Errorf("%s tokenFile %q is not an absolute, clean path", what, tokenFile)
	}

	return nil
}

func absolute(p string) bool {
	return path.IsAbs(p) && path.Clean(p) == p
}
