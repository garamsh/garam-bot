package reference

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

func TestRecovering_AnswersAPanicWith503(t *testing.T) {
	handler := recovering(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/panics" {
			panic("a handler's invariant broke")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	answer := httptest.NewRecorder()
	handler.ServeHTTP(answer, httptest.NewRequest(http.MethodGet, "/panics", nil))
	assert.Equal(t, http.StatusServiceUnavailable, answer.Code)
	var body v1alpha1.ErrorBody
	require.NoError(t, json.Unmarshal(answer.Body.Bytes(), &body))
	assert.Equal(t, v1alpha1.CodeTemporarilyUnavailable, body.Error, "T-3: a panic is answered with a code of Code")

	// Control: a handler that does not panic is answered as it answers.
	answer = httptest.NewRecorder()
	handler.ServeHTTP(answer, httptest.NewRequest(http.MethodGet, "/answers", nil))
	assert.Equal(t, http.StatusNoContent, answer.Code)
}

func TestRecovering_LeavesAnAnsweredResponseAndAnAbortToNetHTTP(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"answered": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			panic("after the answer began")
		},
		"aborted": func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) },
	} {
		assert.Panics(t, func() {
			recovering(handler).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		}, name)
	}
}
