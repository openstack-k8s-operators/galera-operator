package mariadb

// GetLabels -
func GetLabels(name string) map[string]string {
	return map[string]string{
		"owner":     "galera-operator",
		"app":       StatefulSetName(name),
		"cr":        "mariadb-" + name,
		"galera_cr": name,
	}
}

// StatefulSetName - subresource name from a the galera CR
func StatefulSetName(name string) string {
	return name + "-galera"
}

// ResourceName - subresource name from a the galera CR
func ResourceName(name string) string {
	return name + "-galera"
}
