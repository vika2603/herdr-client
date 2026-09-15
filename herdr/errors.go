package herdr

import (
	"errors"
	"fmt"
)

// Op identifies the client operation that failed or was canceled. It does not
// establish whether the server received or executed the request, or whether
// retrying it is safe.
type Op string

const (
	// OpValidate checks local request or frame arguments before encoding.
	OpValidate Op = "validate"
	// OpEncode serializes a request or graphics frame header.
	OpEncode Op = "encode"
	// OpDial establishes a connection.
	OpDial Op = "dial"
	// OpWrite sends a request or graphics frame.
	OpWrite Op = "write"
	// OpRead waits for a response, event, or frame acknowledgement.
	OpRead Op = "read"
	// OpDecode decodes or checks a response or event payload.
	OpDecode Op = "decode"
	// OpClose closes a stream's connection explicitly.
	OpClose Op = "close"
)

// OpError describes a client-side failure in a Herdr protocol operation.
// Method is the wire method, such as "pane.get" or "events.subscribe".
// Err retains the cause for errors.Is and errors.As, including cancellation,
// network and JSON errors, and unknown or unexpected protocol types.
//
// An error response from Herdr remains *Error instead. Context-free helpers
// such as DecodeResult and DecodeEvent return their own decoding errors.
type OpError struct {
	Method string
	Op     Op
	Err    error
}

func (e *OpError) Error() string {
	if e.Method == "" {
		return fmt.Sprintf("herdr: %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("herdr: %s: %s: %v", e.Method, e.Op, e.Err)
}

// Unwrap exposes the cause without requiring callers to parse error messages.
func (e *OpError) Unwrap() error { return e.Err }

func opError(method string, op Op, err error) error {
	if err == nil {
		return nil
	}
	return &OpError{Method: method, Op: op, Err: err}
}

// Error codes reported by the server in an error response. Comparing a
// Code against a plain string stays valid; these constants only save the
// caller from repeating the spelling.
const (
	ErrCodeInvalidRequest              = "invalid_request"
	ErrCodeInvalidParams               = "invalid_params"
	ErrCodeInternalError               = "internal_error"
	ErrCodeTimeout                     = "timeout"
	ErrCodeNotFound                    = "not_found"
	ErrCodePaneNotFound                = "pane_not_found"
	ErrCodeWorkspaceNotFound           = "workspace_not_found"
	ErrCodeTabNotFound                 = "tab_not_found"
	ErrCodeAgentNotFound               = "agent_not_found"
	ErrCodePluginNotFound              = "plugin_not_found"
	ErrCodePluginDisabled              = "plugin_disabled"
	ErrCodePluginPaneNotFound          = "plugin_pane_not_found"
	ErrCodePlatformUnsupported         = "platform_unsupported"
	ErrCodeFeatureDisabled             = "feature_disabled"
	ErrCodeUIBusy                      = "ui_busy"
	ErrCodePopupNotOpen                = "popup_not_open"
	ErrCodeStreamConflict              = "stream_conflict"
	ErrCodeStreamClosed                = "stream_closed"
	ErrCodeAgentBlocked                = "agent_blocked"
	ErrCodeAgentPromptStalled          = "agent_prompt_stalled"
	ErrCodeWorkspaceGroupCloseRequired = "workspace_group_close_required"
	ErrCodeUnsupportedInAppMode        = "unsupported_in_app_mode"

	// Codes internal/e2e observed against herdr 0.9.0 while exercising the
	// methods that report them.
	ErrCodeStaleAnnouncement         = "stale_announcement"
	ErrCodeStaleReleaseNotes         = "stale_release_notes"
	ErrCodeConnectionLocalOnly       = "connection_local_only"
	ErrCodeCommandNotFound           = "command_not_found"
	ErrCodeCellSizeUnavailable       = "cell_size_unavailable"
	ErrCodeAgentNotReady             = "agent_not_ready"
	ErrCodeUnsupportedAgentKind      = "unsupported_agent_kind"
	ErrCodeInvalidAgentName          = "invalid_agent_name"
	ErrCodeInvalidAgentView          = "invalid_agent_view"
	ErrCodeInvalidStateLabel         = "invalid_state_label"
	ErrCodeStaleContent              = "stale_content"
	ErrCodeStaleTarget               = "stale_target"
	ErrCodeUnsupportedEventWaitMatch = "unsupported_event_wait_match"
)

// Error is an error response from the server. Method is the method that was
// called; it is empty when the error did not originate from a call.
type Error struct {
	Method  string
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Method == "" {
		return e.Code + ": " + e.Message
	}
	return e.Method + ": " + e.Code + ": " + e.Message
}

// IsCode reports whether err is, or wraps, a server *Error carrying code.
func IsCode(err error, code string) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Code == code
}
