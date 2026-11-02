//go:build windows

package state

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// restrictedSDDL grants full control to SYSTEM and Administrators only and
// blocks inheritance from the parent (protected DACL).
const restrictedSDDL = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// secureDir limits the state directory to SYSTEM and Administrators. It is only
// applied from an elevated process: an unprivileged user locking their own
// directory to administrators would lock themselves out.
func secureDir(dir string) (applied bool, err error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return false, nil
	}
	sd, err := windows.SecurityDescriptorFromString(restrictedSDDL)
	if err != nil {
		return false, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, err
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		return false, fmt.Errorf("restrict %s: %w", dir, err)
	}
	return true, nil
}
