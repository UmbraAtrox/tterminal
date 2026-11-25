package types

import "time"

type BackendKind string

const (
	BackendPTY    BackendKind = "pty"
	BackendSerial BackendKind = "serial"
	BackendTmux   BackendKind = "tmux"
)

type BackendCaps struct {
	HasProcess    bool `json:"has_process"`
	CanSignal     bool `json:"can_signal"`
	CanResize     bool `json:"can_resize"`
	CanReportExit bool `json:"can_report_exit"`
	CanBreak      bool `json:"can_break"`
	CanSetModem   bool `json:"can_set_modem"`
}

type ExitStatus struct {
	Exited bool   `json:"exited"`
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
}

type BackendStatus struct {
	State     string      `json:"state"`
	Exit      ExitStatus  `json:"exit"`
	PID       int         `json:"pid,omitempty"`
	StartedAt time.Time   `json:"started_at,omitempty"`
	EndedAt   time.Time   `json:"ended_at,omitempty"`
	Detail    string      `json:"detail,omitempty"`
	Caps      BackendCaps `json:"caps"`
}

type Role string

const (
	RoleDeny       Role = "deny"
	RoleReader     Role = "reader"
	RoleWriter     Role = "writer"
	RoleController Role = "controller"
	RoleOwner      Role = "owner"
)

func (r Role) Rank() int {
	switch r {
	case RoleReader:
		return 1
	case RoleWriter:
		return 2
	case RoleController:
		return 3
	case RoleOwner:
		return 4
	default:
		return 0
	}
}

type Identity struct {
	UID   int    `json:"uid"`
	GID   int    `json:"gid"`
	PID   int    `json:"pid"`
	Token string `json:"-"`
}

type Marker struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	Code   int       `json:"code,omitempty"`
	At     time.Time `json:"at"`
	Cursor int64     `json:"cursor"`
}
