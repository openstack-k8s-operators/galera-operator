package mariadb

import (
	databasev1beta1 "github.com/openstack-k8s-operators/galera-operator/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A shim to interact with other Openstack object until the MariaDB CR is replaced with Galera
func MariaDBShim(m *databasev1beta1.Galera) *databasev1beta1.MariaDB {
	mariadb := &databasev1beta1.MariaDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: databasev1beta1.MariaDBSpec{
			Secret:         m.Spec.Secret,
			StorageClass:   m.Spec.StorageClass,
			StorageRequest: m.Spec.StorageRequest,
			ContainerImage: m.Spec.ContainerImage,
		},
	}
	return mariadb
}
