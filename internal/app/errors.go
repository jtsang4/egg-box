package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
	"eggbox/internal/library"
	"eggbox/internal/subscription"
)

// ErrAuthorizationDeclined means the person said no to the system's prompt
// to set the service up. Nothing was changed.
var ErrAuthorizationDeclined = errors.New("permission to set up the service was not given")

// ErrServiceSetup means the service could not be set up or changed even
// though the person agreed. The platform adds what it knows.
var ErrServiceSetup = errors.New("the service could not be set up")

// Issue is a problem the service found in a configuration.
type Issue struct {
	Path    string
	Code    string
	Message string
	Warning bool
}

// ValidationError is a configuration the service refused, with its findings.
type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	for _, i := range e.Issues {
		if !i.Warning {
			return fmt.Sprintf("the configuration was refused: %s (%s)", i.Message, i.Path)
		}
	}
	return "the configuration was refused"
}

// Explanation is an error described for the interface.
type Explanation struct {
	// Code is stable; the interface translates it ("route_conflict"...).
	Code string
	// Detail is the original text, in English, never containing links or credentials.
	Detail string
	Issues []Issue
}

// Explain classifies an error returned by the service.
func Explain(err error) Explanation {
	if err == nil {
		return Explanation{}
	}
	detail := err.Error()

	var verr *ValidationError
	if errors.As(err, &verr) {
		return Explanation{Code: "invalid_config", Detail: detail, Issues: verr.Issues}
	}
	var oe *agentclient.OpError
	if errors.As(err, &oe) {
		ex := Explanation{Code: codeOfAgent(oe.Code), Detail: detail}
		for _, i := range oe.Issues {
			ex.Issues = append(ex.Issues, Issue{Path: i.GetPath(), Code: i.GetCode(), Message: i.GetMessage(), Warning: i.GetSeverity() == agentapi.Issue_SEVERITY_WARNING})
		}
		if oe.RolledBack {
			ex.Code = "rolled_back"
		}
		return ex
	}
	var fe *subscription.FetchError
	if errors.As(err, &fe) {
		return Explanation{Code: "fetch_" + string(fe.Kind), Detail: detail}
	}
	switch {
	case errors.Is(err, ErrNoNodes), isNoNodes(err):
		return Explanation{Code: "no_nodes", Detail: detail}
	case errors.Is(err, subscription.ErrEmpty), errors.Is(err, subscription.ErrUnknownFormat):
		return Explanation{Code: "bad_subscription", Detail: detail}
	case errors.Is(err, agentclient.ErrNotReady):
		return Explanation{Code: "agent_unreachable", Detail: detail}
	case errors.Is(err, ErrAuthorizationDeclined):
		return Explanation{Code: "authorization_declined", Detail: detail}
	case errors.Is(err, ErrServiceSetup):
		return Explanation{Code: "service_setup", Detail: detail}
	case errors.Is(err, library.ErrNotFound):
		return Explanation{Code: "not_found", Detail: detail}
	case errors.Is(err, context.DeadlineExceeded):
		return Explanation{Code: "timeout", Detail: detail}
	case errors.Is(err, context.Canceled):
		return Explanation{Code: "canceled", Detail: detail}
	}
	// Anything else is a message already written for people.
	if strings.TrimSpace(detail) == "" {
		detail = "unknown error"
	}
	return Explanation{Code: "unknown", Detail: detail}
}

func codeOfAgent(c agentapi.ErrorCode) string {
	switch c {
	case agentapi.ErrorCode_ERROR_CODE_INVALID_ARGUMENT:
		return "invalid_argument"
	case agentapi.ErrorCode_ERROR_CODE_INVALID_CONFIG:
		return "invalid_config"
	case agentapi.ErrorCode_ERROR_CODE_POLICY_DENIED:
		return "policy_denied"
	case agentapi.ErrorCode_ERROR_CODE_REVISION_CONFLICT:
		return "revision_conflict"
	case agentapi.ErrorCode_ERROR_CODE_NO_CONFIG:
		return "no_config"
	case agentapi.ErrorCode_ERROR_CODE_NOT_CONNECTED:
		return "not_connected"
	case agentapi.ErrorCode_ERROR_CODE_BUSY:
		return "busy"
	case agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT:
		return "route_conflict"
	case agentapi.ErrorCode_ERROR_CODE_CORE_START_FAILED:
		return "core_start_failed"
	case agentapi.ErrorCode_ERROR_CODE_PLATFORM:
		return "platform"
	case agentapi.ErrorCode_ERROR_CODE_CLEANUP_FAILED:
		return "cleanup_failed"
	case agentapi.ErrorCode_ERROR_CODE_CANCELLED:
		return "canceled"
	case agentapi.ErrorCode_ERROR_CODE_NOT_FOUND:
		return "not_found"
	case agentapi.ErrorCode_ERROR_CODE_UNAVAILABLE:
		return "agent_unreachable"
	}
	return "internal"
}
