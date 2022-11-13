package environment

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Azure/go-ansiterm/winterm"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func (env *ShellEnvironment) Root() bool {
	defer env.Trace(time.Now(), "Root")
	var sid *windows.SID

	// Although this looks scary, it is directly copied from the
	// official windows documentation. The Go API for this is a
	// direct wrap around the official C++ API.
	// See https://docs.microsoft.com/en-us/windows/desktop/api/securitybaseapi/nf-securitybaseapi-checktokenmembership
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid)
	if err != nil {
		env.Log(Error, "Root", err.Error())
		return false
	}
	defer func() {
		_ = windows.FreeSid(sid)
	}()

	// This appears to cast a null pointer so I'm not sure why this
	// works, but this guy says it does and it Works for Me™:
	// https://github.com/golang/go/issues/28804#issuecomment-438838144
	token := windows.Token(0)

	member, err := token.IsMember(sid)
	if err != nil {
		env.Log(Error, "Root", err.Error())
		return false
	}

	return member
}

func (env *ShellEnvironment) Home() string {
	home := os.Getenv("HOME")
	defer func() {
		env.Log(Debug, "Home", home)
	}()
	if len(home) > 0 {
		return home
	}
	// fallback to older implemenations on Windows
	home = os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
	if home == "" {
		home = os.Getenv("USERPROFILE")
	}
	return home
}

func (env *ShellEnvironment) QueryWindowTitles(processName, windowTitleRegex string) (string, error) {
	defer env.Trace(time.Now(), "WindowTitle", windowTitleRegex)
	title, err := queryWindowTitles(processName, windowTitleRegex)
	if err != nil {
		env.Log(Error, "QueryWindowTitles", err.Error())
	}
	return title, err
}

func (env *ShellEnvironment) IsWsl() bool {
	defer env.Trace(time.Now(), "IsWsl")
	return false
}

func (env *ShellEnvironment) IsWsl2() bool {
	defer env.Trace(time.Now(), "IsWsl2")
	return false
}

func (env *ShellEnvironment) TerminalWidth() (int, error) {
	defer env.Trace(time.Now(), "TerminalWidth")
	if env.CmdFlags.TerminalWidth != 0 {
		return env.CmdFlags.TerminalWidth, nil
	}
	handle, err := syscall.Open("CONOUT$", syscall.O_RDWR, 0)
	if err != nil {
		env.Log(Error, "TerminalWidth", err.Error())
		return 0, err
	}
	info, err := winterm.GetConsoleScreenBufferInfo(uintptr(handle))
	if err != nil {
		env.Log(Error, "TerminalWidth", err.Error())
		return 0, err
	}
	// return int(float64(info.Size.X) * 0.57), nil
	return int(info.Size.X), nil
}

func (env *ShellEnvironment) Platform() string {
	return WINDOWS
}

func (env *ShellEnvironment) CachePath() string {
	defer env.Trace(time.Now(), "CachePath")
	// get LOCALAPPDATA if present
	if cachePath := returnOrBuildCachePath(env.Getenv("LOCALAPPDATA")); len(cachePath) != 0 {
		return cachePath
	}
	return env.Home()
}

func (env *ShellEnvironment) LookWinAppPath(file string) (string, error) {
	winAppPath := filepath.Join(env.Getenv("LOCALAPPDATA"), `\Microsoft\WindowsApps\`)
	command := file + ".exe"
	isWinStoreApp := func() bool {
		return env.HasFilesInDir(winAppPath, command)
	}
	if isWinStoreApp() {
		commandFile := filepath.Join(winAppPath, command)
		return readWinAppLink(commandFile)
	}
	return "", errors.New("no Windows Store App")
}

// Takes a registry path to a key like
//
//	"HKLM\Software\Microsoft\Windows NT\CurrentVersion\EditionID"
//
// The last part of the path is the key to retrieve.
//
// If the path ends in "\", the "(Default)" key in that path is retrieved.
//
// Returns a variant type if successful; nil and an error if not.
func (env *ShellEnvironment) WindowsRegistryKeyValue(path string) (*WindowsRegistryValue, error) {
	env.Trace(time.Now(), "WindowsRegistryKeyValue", path)

	// Format:
	// "HKLM\Software\Microsoft\Windows NT\CurrentVersion\EditionID"
	//   1  |                  2                         |   3
	//
	// Split into:
	//
	// 1. Root key - extract the root HKEY string and turn this into a handle to get started
	// 2. Path - open this path
	// 3. Key - get this key value
	//
	// If 3 is "" (i.e. the path ends with "\"), then get (Default) key.
	//
	rootKey, regPath, found := strings.Cut(path, `\`)
	if !found {
		errorLogMsg := fmt.Sprintf("Error, malformed registry path: '%s'", path)
		env.Log(Error, "WindowsRegistryKeyValue", errorLogMsg)
		return nil, errors.New(errorLogMsg)
	}

	regKey := Base(env, regPath)
	if len(regKey) != 0 {
		regPath = strings.TrimSuffix(regPath, `\`+regKey)
	}

	var key registry.Key
	switch rootKey {
	case "HKCR", "HKEY_CLASSES_ROOT":
		key = windows.HKEY_CLASSES_ROOT
	case "HKCC", "HKEY_CURRENT_CONFIG":
		key = windows.HKEY_CURRENT_CONFIG
	case "HKCU", "HKEY_CURRENT_USER":
		key = windows.HKEY_CURRENT_USER
	case "HKLM", "HKEY_LOCAL_MACHINE":
		key = windows.HKEY_LOCAL_MACHINE
	case "HKU", "HKEY_USERS":
		key = windows.HKEY_USERS
	default:
		errorLogMsg := fmt.Sprintf("Error, unknown registry key: '%s'", rootKey)
		env.Log(Error, "WindowsRegistryKeyValue", errorLogMsg)
		return nil, errors.New(errorLogMsg)
	}

	k, err := registry.OpenKey(key, regPath, registry.READ)
	if err != nil {
		env.Log(Error, "WindowsRegistryKeyValue", err.Error())
		return nil, err
	}
	_, valType, err := k.GetValue(regKey, nil)
	if err != nil {
		env.Log(Error, "WindowsRegistryKeyValue", err.Error())
		return nil, err
	}

	var regValue *WindowsRegistryValue

	switch valType {
	case windows.REG_SZ, windows.REG_EXPAND_SZ:
		value, _, _ := k.GetStringValue(regKey)
		regValue = &WindowsRegistryValue{ValueType: STRING, String: value}
	case windows.REG_DWORD:
		value, _, _ := k.GetIntegerValue(regKey)
		regValue = &WindowsRegistryValue{ValueType: DWORD, DWord: value, String: fmt.Sprintf("0x%08X", value)}
	case windows.REG_QWORD:
		value, _, _ := k.GetIntegerValue(regKey)
		regValue = &WindowsRegistryValue{ValueType: QWORD, QWord: value, String: fmt.Sprintf("0x%016X", value)}
	case windows.REG_BINARY:
		value, _, _ := k.GetBinaryValue(regKey)
		regValue = &WindowsRegistryValue{ValueType: BINARY, String: string(value)}
	}

	if regValue == nil {
		errorLogMsg := fmt.Sprintf("Error, no formatter for type: %d", valType)
		return nil, errors.New(errorLogMsg)
	}
	env.Log(Debug, "WindowsRegistryKeyValue", fmt.Sprintf("%s(%s): %s", regKey, regValue.ValueType, regValue.String))
	return regValue, nil
}

func (env *ShellEnvironment) InWSLSharedDrive() bool {
	return false
}

func (env *ShellEnvironment) ConvertToWindowsPath(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

func (env *ShellEnvironment) ConvertToLinuxPath(path string) string {
	return path
}

func (env *ShellEnvironment) DirIsWritable(path string) bool {
	defer env.Trace(time.Now(), "DirIsWritable")
	info, err := os.Stat(path)
	if err != nil {
		env.Log(Error, "DirIsWritable", err.Error())
		return false
	}

	if !info.IsDir() {
		env.Log(Error, "DirIsWritable", "Path isn't a directory")
		return false
	}

	// Check if the user bit is enabled in file permission
	if info.Mode().Perm()&(1<<(uint(7))) == 0 {
		env.Log(Error, "DirIsWritable", "Write permission bit is not set on this file for user")
		return false
	}

	return true
}

func (env *ShellEnvironment) Connection(connectionType ConnectionType) (*Connection, error) {
	if env.networks == nil {
		networks := env.getConnections()
		if len(networks) == 0 {
			return nil, errors.New("No connections found")
		}
		env.networks = networks
	}
	for _, network := range env.networks {
		if network.Type == connectionType {
			return network, nil
		}
	}
	env.Log(Error, "network", fmt.Sprintf("Network type '%s' not found", connectionType))
	return nil, &NotImplemented{}
}