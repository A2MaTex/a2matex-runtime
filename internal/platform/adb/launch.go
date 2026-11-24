package adb

import (
	"context"
	"regexp"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// LaunchAppCommand opens the launcher activity of one installed package.
type LaunchAppCommand struct {
	PackageName string
}

func (LaunchAppCommand) deviceCommand() {}

var packageNamePattern = regexp.MustCompile(constants.AndroidPackageNamePattern)
var activityClassPattern = regexp.MustCompile(`^\.?[\p{L}_$][\p{L}\p{N}\p{M}_$]*(?:\.[\p{L}_$][\p{L}\p{N}\p{M}_$]*)*$`)

func (c LaunchAppCommand) Valid() bool {
	return len(c.PackageName) >= constants.MinAndroidPackageNameLength &&
		len(c.PackageName) <= constants.MaxAndroidPackageNameLength && packageNamePattern.MatchString(c.PackageName)
}

func resolveLauncherArgs(packageName string) []string {
	return []string{"shell", "-T", "cmd", "package", "resolve-activity", "--brief", "--user", "current",
		"-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER", "-p", packageName}
}

func launchArgs(component string) []string {
	// ADB joins shell arguments; quote valid Java class names containing '$' too.
	return []string{"shell", "-T", "am", "start", "-W", "--user", "current", "-a", "android.intent.action.MAIN",
		"-c", "android.intent.category.LAUNCHER", "-n", "'" + component + "'"}
}

func (d *Dispatcher) launchApp(ctx context.Context, serial string, command LaunchAppCommand) error {
	if !command.Valid() {
		return xerror.WithOperation("validate_command",
			xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "invalid Android package name"})
	}
	result, err := d.runDevice(ctx, serial, "resolve_launcher_activity",
		resolveLauncherArgs(command.PackageName), constants.ProcessOutputLimitBytes, 0)
	if err != nil {
		return err
	}
	component, err := parseLauncherActivity(command.PackageName, result)
	if err != nil {
		return err
	}
	// Reuse the attempt context: resolution does not give the launch a fresh timeout.
	result, err = d.runDevice(ctx, serial, "launch_app", launchArgs(component), constants.ProcessOutputLimitBytes, 0)
	if err != nil {
		return err
	}
	err = parseLaunchResult(command.PackageName, result)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return xerror.WithOperation("parse_launch_result", ctxErr)
	}
	return err
}

func parseLauncherActivity(packageName string, result processrunner.Result) (string, error) {
	if result.StdoutTruncated || result.StderrTruncated {
		return "", launcherResolutionFailure("resolution_output_truncated", "Android launcher resolution output exceeded the capture limit")
	}
	if strings.TrimSpace(string(result.Stderr)) != "" {
		return "", launcherResolutionFailure("resolution_rejected", "Android could not resolve the app's launcher activity")
	}
	var component string
	for line := range strings.Lines(string(result.Stdout)) {
		line = strings.TrimSpace(line)
		if line == "" || (component == "" && strings.HasPrefix(line, "priority=")) {
			continue
		}
		if line == "No activity found" {
			return "", launcherResolutionFailure("activity_unresolved", "Android couldn't find a launcher activity for this package. Check that the app is installed and enabled for the current Android user.")
		}
		activityPackage, class, found := strings.Cut(line, "/")
		if component != "" || !found || activityPackage != packageName || !activityClassPattern.MatchString(class) {
			return "", launcherResolutionFailure("invalid_resolved_activity", "Android returned an invalid launcher activity for the requested app")
		}
		component = line
	}
	if component == "" {
		return "", launcherResolutionFailure("activity_unresolved", "Android couldn't find a launcher activity for this package. Check that the app is installed and enabled for the current Android user.")
	}
	return component, nil
}

func launcherResolutionFailure(reason, message string) error {
	return &xerror.OperationError{Operation: "resolve_launcher_activity", Detail: reason,
		Err: xerror.CommandError{Code: constants.CommandFailed, Message: message}}
}

// parseLaunchResult interprets a successful host process using the Android wait-result markers.
func parseLaunchResult(packageName string, result processrunner.Result) error {
	if result.StdoutTruncated || result.StderrTruncated {
		return launchFailure("launch_output_truncated", "Android launch output exceeded the capture limit")
	}
	for line := range strings.Lines(string(result.Stderr)) {
		if err := launchRejection(strings.TrimSpace(line)); err != nil {
			return err
		}
	}

	var status string
	var hasActivity, complete bool
	for line := range strings.Lines(string(result.Stdout)) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if err := launchRejection(line); err != nil {
			return err
		}
		if complete {
			return launchFailure("unexpected_output_after_completion", "Android launch result has output after completion")
		}
		switch {
		case strings.HasPrefix(line, "Status:"):
			if status != "" || (line != "Status: ok" && line != "Status: timeout") {
				return launchFailure("invalid_launch_status", "Android launch status is invalid or repeated")
			}
			status = line
		case strings.HasPrefix(line, "Activity:"):
			component := strings.TrimSpace(strings.TrimPrefix(line, "Activity:"))
			activityPackage, class, found := strings.Cut(component, "/")
			if hasActivity || !found || activityPackage == "" || !activityClassPattern.MatchString(class) {
				return launchFailure("invalid_launch_activity", "Android launch activity is malformed or repeated")
			}
			if activityPackage != packageName && !isAndroidPermissionPrompt(activityPackage, class) {
				return launchFailure("unexpected_activity_package", "Android launch activity does not match the requested package")
			}
			hasActivity = true
		case line == "Complete":
			complete = true
		}
	}
	if !complete || status == "" {
		return launchFailure("incomplete_launch_result", "Android did not report a complete app launch result")
	}
	if status == "Status: timeout" {
		return &xerror.OperationError{
			Operation: "parse_launch_result",
			Detail:    "android_launch_wait_timed_out",
			Err:       xerror.CommandError{Code: constants.CommandTimedOut, Message: "Android timed out waiting for the app launch"},
		}
	}
	return nil
}

// Android can report its permission prompt as the activity reached during launch.
func isAndroidPermissionPrompt(activityPackage, class string) bool {
	const permissionController = "com.android.permissioncontroller"
	return activityPackage == permissionController &&
		strings.TrimPrefix(class, permissionController) == ".permission.ui.GrantPermissionsActivity"
}

func launchRejection(line string) error {
	switch {
	case strings.HasPrefix(line, "Error: Activity not started, unable to resolve"):
		return launchFailure("activity_unresolved", "Android couldn't find an app it could launch for this package name. Check the package name and confirm the app is installed and can be opened from the selected device's launcher.")
	case strings.HasPrefix(line, "Warning: Activity not started because the current activity is being kept for the user"):
		return launchFailure("activity_switch_blocked", "Android rejected the app launch")
	case strings.HasPrefix(line, "Warning: Activity not started because intent should be handled by the caller"):
		return launchFailure("intent_returned_to_caller", "Android rejected the app launch")
	case strings.HasPrefix(line, "Error:") || line == "Error type" || strings.HasPrefix(line, "Error type "):
		return launchFailure("android_rejected_launch", "Android rejected the app launch")
	default:
		return nil
	}
}

func launchFailure(reason, message string) error {
	return &xerror.OperationError{
		Operation: "parse_launch_result",
		Detail:    reason,
		Err:       xerror.CommandError{Code: constants.CommandFailed, Message: message},
	}
}
