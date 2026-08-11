package domain

type BackgroundRuntimeState string

const (
	BackgroundRuntimeRunning       BackgroundRuntimeState = "running"
	BackgroundRuntimeStopped       BackgroundRuntimeState = "stopped"
	BackgroundRuntimeTransitioning BackgroundRuntimeState = "transitioning"
)

type BackgroundRuntime struct {
	State BackgroundRuntimeState
}
