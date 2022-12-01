package mariadb

import (
	databasev1beta1 "github.com/openstack-k8s-operators/galera-operator/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Headless service to give pods connectivity via DNS
func HeadlessService(m *databasev1beta1.Galera) *corev1.Service {
	name := ResourceName(m.Name)
	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: m.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type:      "ClusterIP",
			ClusterIP: "None",
			Ports: []corev1.ServicePort{
				{Name: "mysql", Protocol: "TCP", Port: 3306},
			},
			Selector: map[string]string{
				"app": name,
			},
			// This is required to let pod communicate when
			// they are still in Starting state
			PublishNotReadyAddresses: true,
		},
	}
	return dep
}

// The MySQL Service provided by the Galera cluster
func Service(m *databasev1beta1.Galera) *corev1.Service {
	res := ResourceName(m.Name)
	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
			Labels: map[string]string{
				"app": "mariadb",
			},
		},
		Spec: corev1.ServiceSpec{
			Type: "ClusterIP",
			Ports: []corev1.ServicePort{
				{Name: "mysql", Protocol: "TCP", Port: 3306},
			},
			Selector: map[string]string{
				"app": res,
			},
		},
	}
	return dep
}
