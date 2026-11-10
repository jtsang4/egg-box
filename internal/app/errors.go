package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
	"eggbox/internal/library"
	"eggbox/internal/routing"
	"eggbox/internal/ruledata"
	"eggbox/internal/rulefmt"
	"eggbox/internal/ruleurl"
	"eggbox/internal/subscription"
)

// ErrAuthorizationDeclined means the service is still waiting for the
// person's approval when the authorization wait ends.
var ErrAuthorizationDeclined = errors.New("permission to set up the service was not given")

// ErrServiceSetup means the service could not be set up or reached. The
// platform adds the system's error details.
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
		// Running out of private subnets is reported as a route conflict, but
		// taking over another VPN's routes cannot solve it.
		if oe.Code == agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT && oe.Details["kind"] == "no_free_subnet" {
			ex.Code = "no_free_subnet"
		}
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
	if code := routingCode(err); code != "" {
		return Explanation{Code: code, Detail: detail}
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
	case errors.Is(err, library.ErrNotFound), errors.Is(err, library.ErrNoNode):
		return Explanation{Code: "not_found", Detail: detail}
	case errors.Is(err, library.ErrBadLink):
		return Explanation{Code: "node_bad_link", Detail: detail}
	case errors.Is(err, library.ErrNodeTaken):
		return Explanation{Code: "node_taken", Detail: detail}
	case errors.Is(err, library.ErrNotImported):
		return Explanation{Code: "node_not_imported", Detail: detail}
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

// routingCode classifies what can go wrong with a person's routing choices
// and the rule files behind them; "" when err is neither.
func routingCode(err error) string {
	var status ruledata.StatusError
	var parse *ruledata.ParseError
	switch {
	case errors.Is(err, routing.ErrDuplicateName):
		return "item_duplicate_name"
	case errors.Is(err, routing.ErrNoName):
		return "item_no_name"
	case errors.Is(err, routing.ErrBadEntry):
		return "entry_invalid"
	case errors.Is(err, routing.ErrBadResolver):
		return "resolver_invalid"
	case errors.Is(err, routing.ErrResolverNotHost):
		return "resolver_not_host"
	case errors.Is(err, routing.ErrLocked):
		return "plan_locked"
	case errors.Is(err, routing.ErrNotMine):
		return "plan_not_mine"
	case errors.Is(err, routing.ErrTooManyExceptions):
		return "too_many_exceptions"
	case errors.Is(err, routing.ErrExceptionsChanged):
		return "exceptions_changed"
	case errors.Is(err, routing.ErrNoItem), errors.Is(err, routing.ErrNoException), errors.Is(err, routing.ErrNoPlan), errors.Is(err, routing.ErrNoSource):
		return "not_found"
	case errors.Is(err, routing.ErrDuplicateSource):
		return "source_duplicate"
	case errors.Is(err, routing.ErrLastSource):
		return "source_last"
	case errors.Is(err, routing.ErrNotInline):
		return "source_not_inline"
	case errors.Is(err, routing.ErrNoEntries):
		return "entries_empty"
	case errors.Is(err, routing.ErrTooManyEntries):
		return "entries_too_many"
	case errors.Is(err, ErrBadTarget):
		return "bad_target"
	case errors.Is(err, ErrLooksLikeSubscription):
		return "looks_like_subscription"
	case errors.Is(err, ruleurl.ErrAddress):
		return "rule_bad_address"
	case errors.Is(err, ruledata.ErrTooLarge):
		return "rule_too_large"
	case errors.Is(err, rulefmt.ErrEmpty):
		return "rule_empty"
	case errors.Is(err, rulefmt.ErrUnrecognized), errors.Is(err, rulefmt.ErrMalformed), errors.Is(err, rulefmt.ErrMRS), errors.As(err, &parse):
		return "rule_unreadable"
	case errors.As(err, &status):
		return "rule_http_status"
	}
	return ""
}
