package installer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestResetServiceAbsenceIsTyped(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			options := ServiceStatusOptions{Platform: platform, BinaryPath: "/not-used", Runner: resetTestRunner(func(string, ...string) ([]byte, error) { return nil, errors.New("permission denied") })}
			if _, err := InspectService(t.Context(), options); err == nil {
				t.Fatal("command failure treated as absence")
			}
			options.Runner = resetTestRunner(func(string, ...string) ([]byte, error) {
				if platform == "linux" {
					return []byte("LoadState=not-found\n"), nil
				}
				return nil, ErrServiceAbsent
			})
			state, err := InspectService(t.Context(), options)
			if err != nil || !state.Absent {
				t.Fatalf("typed absence: %+v %v", state, err)
			}
			if err := StopService(t.Context(), options); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResetServiceStopsOnlySelectedJob(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			options := resetTestOptions(t)
			loaded := true
			var calls []string
			runner := resetTestRunner(func(name string, args ...string) ([]byte, error) {
				command := name + " " + strings.Join(args, " ")
				calls = append(calls, command)
				if strings.Contains(command, "bootout") || strings.Contains(command, " stop ") {
					loaded = false
					return nil, nil
				}
				if strings.Contains(command, "disable") {
					return nil, nil
				}
				if !loaded {
					if platform == "linux" {
						return []byte("LoadState=not-found\n"), nil
					}
					return nil, ErrServiceAbsent
				}
				if platform == "darwin" {
					return []byte(fmt.Sprintf("program = %s\narguments = {\n%s\ndaemon\n}\nstate = waiting\n", options.ExecutablePath, options.ExecutablePath)), nil
				}
				return []byte(fmt.Sprintf("LoadState=loaded\nActiveState=inactive\nMainPID=0\nExecStart={ path=%s ; argv[]=%s daemon ; }\n", options.ExecutablePath, options.ExecutablePath)), nil
			})
			if err := StopService(context.Background(), ServiceStatusOptions{Platform: platform, BinaryPath: options.ExecutablePath, UserID: 12345, Runner: runner}); err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(calls, "\n")
			if platform == "darwin" && !strings.Contains(joined, "launchctl bootout gui/12345/io.goodkind.agent-gate") {
				t.Fatalf("wrong job target: %s", joined)
			}
			if platform == "linux" && (!strings.Contains(joined, "systemctl --user stop agent-gate.service") || !strings.Contains(joined, "systemctl --user disable agent-gate.service")) {
				t.Fatalf("wrong service control: %s", joined)
			}
		})
	}
}
