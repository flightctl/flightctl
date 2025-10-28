package v1alpha1

// Role constants for flightctl user roles
const (
	RoleAdmin     = "admin"     // Full access to all resources
	RoleOperator  = "operator"  // Manage devices, fleets, resourcesyncs
	RoleViewer    = "viewer"    // Read-only access to devices, fleets, resourcesyncs
	RoleInstaller = "installer" // Limited access for device installation
)
