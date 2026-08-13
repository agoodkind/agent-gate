package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	configDirectoryMode = 0o700
	configFileMode      = 0o600
)

// DefaultsPlan retains validated configuration bytes for atomic application.
type DefaultsPlan struct {
	Path         string
	Content      []byte
	Config       *Config
	applyPath    string
	content      []byte
	path         string
	pathState    defaultsPathState
	beforeRename func()
	applyMutex   sync.Mutex
	consumed     bool
}

type defaultsPathState struct {
	applyPath  string
	components []defaultsPathComponent
	handles    map[string]*os.File
}

type defaultsPathComponent struct {
	path                   string
	exists                 bool
	allowDirectoryCreation bool
	info                   os.FileInfo
	birthTime              defaultsBirthTime
}

type defaultsBirthTime struct {
	seconds     int64
	nanoseconds int64
	available   bool
}

// PrepareDefaults reads and validates the complete replacement without writing it.
func PrepareDefaults(options EnsureDefaultsOptions) (*DefaultsPlan, error) {
	configPath := filepath.Clean(Path())
	log := slog.Default()
	log.Info(
		"config prepare defaults",
		"path", configPath,
		"auto_update", options.AutoUpdateMode,
		"audit_profile", options.AuditProfile,
	)
	if err := validateDefaultsOptions(options); err != nil {
		log.Warn("config prepare defaults rejected options", "path", configPath, "err", err)
		return nil, err
	}
	initialPathState, err := captureDefaultsPathState(configPath)
	if err != nil {
		return nil, err
	}

	current, err := os.ReadFile(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read config: %w", err)
	}
	content := string(current)
	if errors.Is(err, os.ErrNotExist) {
		content = defaultConfigTOML
	}
	content, err = mergeUpdateDefaults(content, options.AutoUpdateMode)
	if err != nil {
		return nil, err
	}
	content, err = mergeAuditStorageDefaults(content, options.AuditProfile)
	if err != nil {
		return nil, err
	}
	preparedBytes := []byte(content)
	preparedConfig, err := loadSource(configPath, preparedBytes, true)
	if err != nil {
		return nil, err
	}
	if options.AuditProfile != "" &&
		preparedConfig.AuditStoragePolicy().Profile != options.AuditProfile {
		return nil, fmt.Errorf(
			"prepared audit storage profile = %q, want %q",
			preparedConfig.AuditStoragePolicy().Profile,
			options.AuditProfile,
		)
	}
	pathState, err := captureDefaultsPathState(configPath)
	if err != nil {
		return nil, err
	}
	if !sameDefaultsPathState(initialPathState, pathState) {
		return nil, reportDefaultsPreparationError(
			"revalidate config path",
			errors.New("config path identity changed during preparation"),
		)
	}
	if err := retainDefaultsPathHandles(&pathState); err != nil {
		return nil, err
	}
	plan := &DefaultsPlan{
		Path:         configPath,
		Content:      append([]byte(nil), preparedBytes...),
		Config:       preparedConfig,
		applyPath:    pathState.applyPath,
		content:      append([]byte(nil), preparedBytes...),
		path:         configPath,
		pathState:    pathState,
		beforeRename: nil,
		applyMutex:   sync.Mutex{},
		consumed:     false,
	}
	runtime.SetFinalizer(plan, closeDefaultsPlanHandles)
	return plan, nil
}

// ApplyDefaults atomically replaces the configuration with retained bytes.
func ApplyDefaults(plan *DefaultsPlan) (string, error) {
	slog.Info("config apply defaults", "has_plan", plan != nil)
	if err := validateDefaultsPlan(plan); err != nil {
		return "", err
	}
	if err := beginDefaultsPlanUse(plan); err != nil {
		return plan.path, err
	}
	defer plan.applyMutex.Unlock()
	defer plan.pathState.closeHandles()
	applyPath := plan.applyPath
	if applyPath == "" {
		applyPath = plan.path
	}
	if err := validateDefaultsApplyPath(plan.path, plan.pathState); err != nil {
		return plan.path, err
	}
	parentPath := filepath.Dir(applyPath)
	if err := os.MkdirAll(parentPath, configDirectoryMode); err != nil {
		return plan.path, reportDefaultsApplyError("create config dir", err)
	}
	writePathState, err := validatePreparedDefaultsApplyPath(plan.path, plan.pathState)
	if err != nil {
		return plan.path, err
	}
	directory, err := openDefaultsApplyDirectory(parentPath, writePathState)
	if err != nil {
		return plan.path, err
	}
	defer func() { _ = directory.Close() }()
	tempFile, tempName, err := createDefaultsTempFile(directory, filepath.Base(applyPath))
	if err != nil {
		return plan.path, reportDefaultsApplyError("create config temp file", err)
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = unix.Unlinkat(int(directory.Fd()), tempName, 0)
		}
	}()
	if err := tempFile.Chmod(defaultsApplyFileMode(writePathState)); err != nil {
		_ = tempFile.Close()
		return plan.path, reportDefaultsApplyError("set config temp file mode", err)
	}
	if _, err := tempFile.Write(plan.content); err != nil {
		_ = tempFile.Close()
		return plan.path, reportDefaultsApplyError("write config temp file", err)
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		return plan.path, reportDefaultsApplyError("synchronize config temp file", err)
	}
	if err := tempFile.Close(); err != nil {
		return plan.path, reportDefaultsApplyError("close config temp file", err)
	}
	if err := validateDefaultsApplyPath(plan.path, writePathState); err != nil {
		return plan.path, err
	}
	if plan.beforeRename != nil {
		plan.beforeRename()
	}
	replacement, cleanupTemp, err := beginDefaultsTargetReplacement(
		directory,
		tempName,
		filepath.Base(applyPath),
		defaultsPathInfo(writePathState, applyPath),
	)
	removeTemp = cleanupTemp
	if err != nil {
		return plan.path, err
	}
	if err := validateDefaultsApplyParents(plan.path, writePathState); err != nil {
		rollbackErr := replacement.rollback()
		joinedErr := errors.Join(err, rollbackErr)
		slog.Warn("config replacement rejected", "err", joinedErr)
		return plan.path, joinedErr
	}
	if err := replacement.commit(); err != nil {
		return plan.path, err
	}
	if err := directory.Sync(); err != nil {
		return plan.path, reportDefaultsApplyError("synchronize config directory", err)
	}
	return plan.path, nil
}

func validateDefaultsPlan(plan *DefaultsPlan) error {
	if plan == nil {
		return errors.New("configuration plan is required")
	}
	if plan.path == "" {
		return errors.New("configuration plan path is required")
	}
	return nil
}

func beginDefaultsPlanUse(plan *DefaultsPlan) error {
	plan.applyMutex.Lock()
	if plan.consumed {
		plan.applyMutex.Unlock()
		return errors.New("configuration plan has already been consumed")
	}
	plan.consumed = true
	runtime.SetFinalizer(plan, nil)
	return nil
}

// Close releases a prepared plan without applying it.
func (plan *DefaultsPlan) Close() error {
	if plan == nil {
		return nil
	}
	plan.applyMutex.Lock()
	defer plan.applyMutex.Unlock()
	if plan.consumed {
		return nil
	}
	plan.consumed = true
	runtime.SetFinalizer(plan, nil)
	plan.pathState.closeHandles()
	return nil
}

func openDefaultsApplyDirectory(
	parentPath string,
	pathState defaultsPathState,
) (*os.File, error) {
	directory, err := os.Open(parentPath)
	if err != nil {
		return nil, reportDefaultsApplyError("open config directory", err)
	}
	directoryInfo, err := directory.Stat()
	if err != nil {
		_ = directory.Close()
		return nil, reportDefaultsApplyError("inspect config directory", err)
	}
	expectedInfo := defaultsPathInfo(pathState, parentPath)
	directoryBirthTime, err := captureDefaultsBirthTimeAt(int(directory.Fd()), ".")
	if err != nil {
		_ = directory.Close()
		return nil, reportDefaultsApplyError("inspect config directory identity", err)
	}
	if expectedInfo == nil || !os.SameFile(expectedInfo.info, directoryInfo) ||
		!sameDefaultsBirthTime(expectedInfo.birthTime, directoryBirthTime) {
		_ = directory.Close()
		return nil, reportDefaultsApplyError(
			"inspect config directory",
			errors.New("config directory identity changed after validation"),
		)
	}
	return directory, nil
}

func createDefaultsTempFile(directory *os.File, targetName string) (*os.File, string, error) {
	for range 100 {
		var randomBytes [8]byte
		if _, err := rand.Read(randomBytes[:]); err != nil {
			wrappedErr := fmt.Errorf("generate config temp name: %w", err)
			slog.Warn("config temp name generation failed", "err", wrappedErr)
			return nil, "", wrappedErr
		}
		name := "." + targetName + "." + hex.EncodeToString(randomBytes[:]) + ".tmp"
		fileDescriptor, err := unix.Openat(
			int(directory.Fd()),
			name,
			unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			uint32(configFileMode),
		)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			wrappedErr := fmt.Errorf("open config temp file: %w", err)
			slog.Warn("config temp file creation failed", "err", wrappedErr)
			return nil, "", wrappedErr
		}
		return os.NewFile(uintptr(fileDescriptor), name), name, nil
	}
	return nil, "", errors.New("could not allocate a unique config temp file")
}

func validateDefaultsOptions(options EnsureDefaultsOptions) error {
	if options.AutoUpdateMode != "" &&
		options.AutoUpdateMode != UpdateModeApply &&
		options.AutoUpdateMode != UpdateModeCheck &&
		options.AutoUpdateMode != "off" {
		return fmt.Errorf(
			"auto-update mode must be %q, %q, or %q",
			UpdateModeCheck,
			UpdateModeApply,
			"off",
		)
	}
	if options.AuditProfile != "" {
		if _, err := auditStorageProfilePolicy(options.AuditProfile); err != nil {
			return err
		}
	}
	return nil
}

func mergeAuditStorageDefaults(
	contents string,
	profile AuditStorageProfile,
) (string, error) {
	if profile != "" {
		if _, err := auditStorageProfilePolicy(profile); err != nil {
			return "", err
		}
	}
	lines := strings.SplitAfter(contents, "\n")
	location := locateAuditStorage(lines)
	if location.profileLine >= 0 {
		if profile == "" {
			return contents, nil
		}
		lines[location.profileLine] = replaceTOMLAssignmentValue(
			lines[location.profileLine],
			string(profile),
		)
		if location.profileEndLine > location.profileLine {
			lines[location.profileLine] = replaceLineEnding(
				lines[location.profileLine],
				location.profileClosingSuffix,
			)
			lines = append(
				lines[:location.profileLine+1],
				lines[location.profileEndLine+1:]...,
			)
		}
		return strings.Join(lines, ""), nil
	}
	if location.inlineLine >= 0 {
		if profile == "" {
			return contents, nil
		}
		updated, err := replaceInlineTableProfile(lines[location.inlineLine], profile)
		if err != nil {
			return "", err
		}
		lines[location.inlineLine] = updated
		return strings.Join(lines, ""), nil
	}
	if profile == "" {
		if location.present {
			return contents, nil
		}
		return appendAuditStorageTable(contents, AuditStorageProfileBalanced), nil
	}
	if location.tableStart >= 0 {
		return insertConfigLine(lines, location.tableStart+1, fmt.Sprintf("profile = %q\n", profile)), nil
	}
	if location.firstNested >= 0 {
		block := fmt.Sprintf("[audit.storage]\nprofile = %q\n\n", profile)
		return insertConfigLine(lines, location.firstNested, block), nil
	}
	if location.assignmentLine >= 0 {
		keyPath := []string{"audit", "storage", "profile"}
		keyPath = keyPath[len(location.assignmentTable):]
		line := fmt.Sprintf("%s = %q\n", strings.Join(keyPath, "."), profile)
		return insertConfigLine(lines, location.assignmentLine, line), nil
	}
	return appendAuditStorageTable(contents, profile), nil
}

type auditStorageLocation struct {
	present              bool
	tableStart           int
	firstNested          int
	profileLine          int
	profileEndLine       int
	profileClosingSuffix string
	assignmentLine       int
	assignmentTable      []string
	inlineLine           int
}

func locateAuditStorage(lines []string) auditStorageLocation {
	location := auditStorageLocation{
		present:              false,
		tableStart:           -1,
		firstNested:          -1,
		profileLine:          -1,
		profileEndLine:       -1,
		profileClosingSuffix: "",
		assignmentLine:       -1,
		assignmentTable:      nil,
		inlineLine:           -1,
	}
	structuralLines := tomlStructuralLines(lines)
	var tablePath []string
	for i := range lines {
		if !structuralLines[i] {
			continue
		}
		headerPath, _, header := tomlTableContextPath(lines[i])
		if header {
			tablePath = headerPath
			if hasTOMLKeyPrefix(headerPath, []string{"audit", "storage"}) {
				location.present = true
				if len(headerPath) == 2 {
					location.tableStart = i
				} else if location.firstNested < 0 {
					location.firstNested = i
				}
			}
			continue
		}
		keyPath, assignment := tomlAssignmentKeyPath(lines[i])
		if !assignment {
			continue
		}
		fullPath := append(append([]string(nil), tablePath...), keyPath...)
		if !hasTOMLKeyPrefix(fullPath, []string{"audit", "storage"}) {
			continue
		}
		location.present = true
		if len(fullPath) == 2 && strings.HasPrefix(strings.TrimSpace(lines[i][tomlUnquotedIndex(lines[i], '=')+1:]), "{") {
			location.inlineLine = i
		}
		if location.assignmentLine < 0 {
			location.assignmentLine = i
			location.assignmentTable = append([]string(nil), tablePath...)
		}
		if len(fullPath) == 3 && fullPath[2] == "profile" {
			location.profileLine = i
			location.profileEndLine, location.profileClosingSuffix = tomlAssignmentValueEnd(
				lines,
				i,
			)
		}
	}
	return location
}

func tomlAssignmentValueEnd(lines []string, start int) (int, string) {
	state := advanceTOMLMultilineState(lines[start], tomlMultilineNone)
	if state == tomlMultilineNone {
		return start, ""
	}
	for i := start + 1; i < len(lines); i++ {
		closing := tomlMultilineClosingIndex(lines[i], state)
		state = advanceTOMLMultilineState(lines[i], state)
		if state == tomlMultilineNone {
			if closing < 0 {
				return i, ""
			}
			return i, lines[i][closing+3:]
		}
	}
	return start, ""
}

func replaceLineEnding(line string, ending string) string {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line + ending
}

func replaceInlineTableProfile(line string, profile AuditStorageProfile) (string, error) {
	equals := tomlUnquotedIndex(line, '=')
	if equals < 0 {
		return "", errors.New("audit storage inline table is missing an assignment")
	}
	open := strings.Index(line[equals+1:], "{")
	if open < 0 {
		return "", errors.New("audit storage inline table is missing an opening brace")
	}
	open += equals + 1
	closingBrace := inlineTableClosingBrace(line, open)
	if closingBrace < 0 {
		return "", errors.New("audit storage inline table is missing a closing brace")
	}
	body := line[open+1 : closingBrace]
	fields := splitInlineTableFields(body)
	for i := range fields {
		keyPath, found := tomlAssignmentKeyPath(fields[i])
		if !found || len(keyPath) != 1 || keyPath[0] != "profile" {
			continue
		}
		fieldEquals := tomlUnquotedIndex(fields[i], '=')
		fields[i] = fields[i][:fieldEquals+1] + " " + strconv.Quote(string(profile))
		return line[:open+1] + strings.Join(fields, ",") + line[closingBrace:], nil
	}
	prefix := " profile = " + strconv.Quote(string(profile))
	if strings.TrimSpace(body) != "" {
		prefix += ","
	}
	return line[:open+1] + prefix + body + line[closingBrace:], nil
}

func inlineTableClosingBrace(line string, open int) int {
	depth := 0
	var quote byte
	escaped := false
	for i := open; i < len(line); i++ {
		current := line[i]
		if quote != 0 {
			if quote == '"' && current == '\\' && !escaped {
				escaped = true
				continue
			}
			if current == quote && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}
		if current == '"' || current == '\'' {
			quote = current
			continue
		}
		switch current {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func splitInlineTableFields(body string) []string {
	var fields []string
	start := 0
	depth := 0
	var quote byte
	escaped := false
	for i := range len(body) {
		current := body[i]
		if quote != 0 {
			if quote == '"' && current == '\\' && !escaped {
				escaped = true
				continue
			}
			if current == quote && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}
		if current == '"' || current == '\'' {
			quote = current
			continue
		}
		switch current {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth == 0 {
				fields = append(fields, body[start:i])
				start = i + 1
			}
		}
	}
	return append(fields, body[start:])
}

func replaceTOMLAssignmentValue(line string, value string) string {
	equals := tomlUnquotedIndex(line, '=')
	if equals < 0 {
		return line
	}
	suffixStart := assignmentValueSuffixStart(line, equals)
	return line[:equals+1] + " " + strconv.Quote(value) + line[suffixStart:]
}

func assignmentValueSuffixStart(line string, equals int) int {
	if comment := tomlUnquotedIndex(line[equals+1:], '#'); comment >= 0 {
		suffixStart := equals + 1 + comment
		for suffixStart > equals+1 && (line[suffixStart-1] == ' ' || line[suffixStart-1] == '\t') {
			suffixStart--
		}
		return suffixStart
	}
	suffixStart := len(line)
	if suffixStart > 0 && line[suffixStart-1] == '\n' {
		suffixStart--
	}
	if suffixStart > 0 && line[suffixStart-1] == '\r' {
		suffixStart--
	}
	for suffixStart > equals+1 && (line[suffixStart-1] == ' ' || line[suffixStart-1] == '\t') {
		suffixStart--
	}
	return suffixStart
}

func validateDefaultsApplyPath(configPath string, expected defaultsPathState) error {
	current, err := captureDefaultsPathState(configPath)
	if err != nil {
		return reportDefaultsApplyError("revalidate config path", err)
	}
	if !sameDefaultsPathState(expected, current) {
		return reportDefaultsApplyError(
			"revalidate config path",
			errors.New("config path identity changed after preparation"),
		)
	}
	return nil
}

func validateDefaultsApplyParents(configPath string, expected defaultsPathState) error {
	current, err := captureDefaultsPathState(configPath)
	if err != nil {
		return reportDefaultsApplyError("revalidate config path", err)
	}
	if !sameDefaultsPathStateExceptTarget(expected, current) {
		return reportDefaultsApplyError(
			"revalidate config path",
			errors.New("config path identity changed after replacement"),
		)
	}
	return nil
}

func validatePreparedDefaultsApplyPath(
	configPath string,
	expected defaultsPathState,
) (defaultsPathState, error) {
	current, err := captureDefaultsPathState(configPath)
	if err != nil {
		return defaultsPathState{}, reportDefaultsApplyError("revalidate config path", err)
	}
	if !validPreparedDefaultsPathTransition(expected, current) {
		return defaultsPathState{}, reportDefaultsApplyError(
			"revalidate config path",
			errors.New("config path identity changed after preparation"),
		)
	}
	current.handles = expected.handles
	return current, nil
}

func captureDefaultsPathState(configPath string) (defaultsPathState, error) {
	state := defaultsPathState{applyPath: "", components: nil, handles: nil}
	currentPath := configPath
	for range 255 {
		if err := appendDefaultsPathAncestors(&state, currentPath); err != nil {
			return defaultsPathState{}, err
		}
		info, err := os.Lstat(currentPath)
		if errors.Is(err, os.ErrNotExist) {
			state.components = append(state.components, defaultsPathComponent{
				path:                   currentPath,
				exists:                 false,
				allowDirectoryCreation: false,
				info:                   nil,
				birthTime: defaultsBirthTime{
					seconds: 0, nanoseconds: 0, available: false,
				},
			})
			state.applyPath = currentPath
			return state, nil
		}
		if err != nil {
			return defaultsPathState{}, reportDefaultsPreparationError(
				"inspect config symlink target",
				err,
			)
		}
		birthTime, err := captureDefaultsBirthTimeAt(unix.AT_FDCWD, currentPath)
		if err != nil {
			return defaultsPathState{}, reportDefaultsPreparationError(
				"inspect config creation identity",
				err,
			)
		}
		state.components = append(state.components, defaultsPathComponent{
			path:                   currentPath,
			exists:                 true,
			allowDirectoryCreation: false,
			info:                   info,
			birthTime:              birthTime,
		})
		if info.Mode()&os.ModeSymlink == 0 {
			state.applyPath = currentPath
			return state, nil
		}
		target, err := os.Readlink(currentPath)
		if err != nil {
			return defaultsPathState{}, reportDefaultsPreparationError("read config symlink", err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(currentPath), target)
		}
		currentPath = filepath.Clean(target)
	}
	return defaultsPathState{}, reportDefaultsPreparationError(
		"resolve config symlink",
		errors.New("too many symbolic links"),
	)
}

func appendDefaultsPathAncestors(state *defaultsPathState, path string) error {
	ancestors := defaultsPathAncestors(path)
	for _, ancestor := range ancestors {
		info, err := os.Lstat(ancestor)
		if errors.Is(err, os.ErrNotExist) {
			state.components = append(state.components, defaultsPathComponent{
				path:                   ancestor,
				exists:                 false,
				allowDirectoryCreation: true,
				info:                   nil,
				birthTime: defaultsBirthTime{
					seconds: 0, nanoseconds: 0, available: false,
				},
			})
			continue
		}
		if err != nil {
			return reportDefaultsPreparationError("inspect config path ancestor", err)
		}
		birthTime, err := captureDefaultsBirthTimeAt(unix.AT_FDCWD, ancestor)
		if err != nil {
			return reportDefaultsPreparationError("inspect config ancestor identity", err)
		}
		state.components = append(state.components, defaultsPathComponent{
			path:                   ancestor,
			exists:                 true,
			allowDirectoryCreation: true,
			info:                   info,
			birthTime:              birthTime,
		})
	}
	return nil
}

func defaultsPathAncestors(path string) []string {
	var ancestors []string
	for current := filepath.Dir(path); current != filepath.Dir(current); current = filepath.Dir(current) {
		ancestors = append(ancestors, current)
	}
	for left, right := 0, len(ancestors)-1; left < right; left, right = left+1, right-1 {
		ancestors[left], ancestors[right] = ancestors[right], ancestors[left]
	}
	return ancestors
}

func sameDefaultsPathState(first defaultsPathState, second defaultsPathState) bool {
	if first.applyPath != second.applyPath || len(first.components) != len(second.components) {
		return false
	}
	for i := range first.components {
		firstComponent := first.components[i]
		secondComponent := second.components[i]
		if firstComponent.path != secondComponent.path ||
			firstComponent.exists != secondComponent.exists ||
			firstComponent.allowDirectoryCreation != secondComponent.allowDirectoryCreation {
			return false
		}
		if firstComponent.exists && !sameDefaultsPathComponent(
			first,
			firstComponent,
			secondComponent,
		) {
			return false
		}
	}
	return true
}

func sameDefaultsPathStateExceptTarget(first defaultsPathState, second defaultsPathState) bool {
	if first.applyPath != second.applyPath || len(first.components) != len(second.components) {
		return false
	}
	for i := range first.components {
		firstComponent := first.components[i]
		secondComponent := second.components[i]
		if firstComponent.path != secondComponent.path ||
			firstComponent.allowDirectoryCreation != secondComponent.allowDirectoryCreation {
			return false
		}
		if firstComponent.path == first.applyPath {
			continue
		}
		if firstComponent.exists != secondComponent.exists {
			return false
		}
		if firstComponent.exists && !sameDefaultsPathComponent(
			first,
			firstComponent,
			secondComponent,
		) {
			return false
		}
	}
	return true
}

func validPreparedDefaultsPathTransition(
	expected defaultsPathState,
	current defaultsPathState,
) bool {
	if expected.applyPath != current.applyPath || len(expected.components) != len(current.components) {
		return false
	}
	for i := range expected.components {
		expectedComponent := expected.components[i]
		currentComponent := current.components[i]
		if expectedComponent.path != currentComponent.path ||
			expectedComponent.allowDirectoryCreation != currentComponent.allowDirectoryCreation {
			return false
		}
		if expectedComponent.exists {
			if !currentComponent.exists ||
				!sameDefaultsPathComponent(expected, expectedComponent, currentComponent) {
				return false
			}
			continue
		}
		if !currentComponent.exists {
			continue
		}
		if !expectedComponent.allowDirectoryCreation ||
			!currentComponent.info.IsDir() ||
			currentComponent.info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func sameDefaultsPathComponent(
	expectedState defaultsPathState,
	expected defaultsPathComponent,
	current defaultsPathComponent,
) bool {
	if handle := expectedState.handles[expected.path]; handle != nil {
		handleInfo, err := handle.Stat()
		return err == nil && os.SameFile(handleInfo, current.info)
	}
	return os.SameFile(expected.info, current.info) &&
		sameDefaultsBirthTime(expected.birthTime, current.birthTime)
}

func sameDefaultsBirthTime(first defaultsBirthTime, second defaultsBirthTime) bool {
	if first.available != second.available {
		return false
	}
	if !first.available {
		return true
	}
	return first.seconds == second.seconds && first.nanoseconds == second.nanoseconds
}

func reportDefaultsPreparationError(operation string, err error) error {
	wrappedErr := fmt.Errorf("%s: %w", operation, err)
	slog.Warn("config defaults preparation failed", "operation", operation, "err", wrappedErr)
	return wrappedErr
}

func appendAuditStorageTable(contents string, profile AuditStorageProfile) string {
	block := fmt.Sprintf("[audit.storage]\nprofile = %q\n", profile)
	separator := "\n"
	if strings.HasSuffix(contents, "\n") {
		separator = ""
	}
	return contents + separator + "\n" + block
}

func insertConfigLine(lines []string, index int, line string) string {
	lines = append(lines[:index], append([]string{line}, lines[index:]...)...)
	return strings.Join(lines, "")
}

func reportDefaultsApplyError(operation string, err error) error {
	wrappedErr := fmt.Errorf("%s: %w", operation, err)
	slog.Warn("config defaults apply failed", "operation", operation, "err", wrappedErr)
	return wrappedErr
}
