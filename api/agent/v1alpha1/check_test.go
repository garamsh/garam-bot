package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
)

func valid() v1alpha1.SessionSpec {
	return v1alpha1.SessionSpec{
		Bot:   v1alpha1.Bot{Name: "mira"},
		Model: v1alpha1.Model{BaseURL: "https://gateway.example", TokenFile: "/run/garam-bot/model/token"},
		Tools: []v1alpha1.Tool{{Name: "memory", URL: "http://127.0.0.1:7701/mcp", TokenFile: "/run/garam-bot/tools/memory"}},
	}
}

func TestCheckSession_SES1(t *testing.T) {
	require.NoError(t, v1alpha1.CheckSession("s", valid())) // control
	assert.Error(t, v1alpha1.CheckSession("S", valid()), "an identifier that is not a DNS label")
	for name, change := range map[string]func(*v1alpha1.SessionSpec){
		"no bot name":            func(s *v1alpha1.SessionSpec) { s.Bot.Name = "" },
		"a relative model URL":   func(s *v1alpha1.SessionSpec) { s.Model.BaseURL = "/v1" },
		"a model URL of ftp":     func(s *v1alpha1.SessionSpec) { s.Model.BaseURL = "ftp://gateway.example" },
		"a relative token file":  func(s *v1alpha1.SessionSpec) { s.Model.TokenFile = "token" },
		"an unclean token file":  func(s *v1alpha1.SessionSpec) { s.Tools[0].TokenFile = "/run/../token" },
		"a tool without a name":  func(s *v1alpha1.SessionSpec) { s.Tools[0].Name = "" },
		"a tool repeated":        func(s *v1alpha1.SessionSpec) { s.Tools = append(s.Tools, s.Tools[0]) },
		"a relative working dir": func(s *v1alpha1.SessionSpec) { s.WorkingDir = "work" },
	} {
		spec := valid()
		change(&spec)
		assert.Error(t, v1alpha1.CheckSession("s", spec), name)
	}
}

func TestTaskRequestCheck_TASK1(t *testing.T) {
	ok := v1alpha1.TaskRequest{ID: "task-1.a_b", Input: []v1alpha1.Input{{Text: "hi"}}}
	require.NoError(t, ok.Check()) // control
	assert.Error(t, v1alpha1.TaskRequest{ID: "a/b", Input: ok.Input}.Check(), "an identifier with a slash")
	assert.Error(t, v1alpha1.TaskRequest{ID: "a"}.Check(), "no input")
}
