package ingress

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func createDeployment(ingress metav1.Object, host string, cookieExpire string, emailDomain string,
	service string, servicePort string, image string, protocol string, port int) *appsv1.Deployment {

	var secure string
	if protocol == "https" {
		secure = "true"
	} else {
		secure = "false"
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ingress.GetName() + "-auth",
			Namespace: ingress.GetNamespace(),
			Labels: map[string]string{
				"k8s-app": ingress.GetName() + "-auth",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"k8s-app": ingress.GetName() + "-auth",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"k8s-app": ingress.GetName() + "-auth",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "auth-proxy",
							Image: image,
							Env: []corev1.EnvVar{
								{
									Name: "CLIENT_ID",
									ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "auth-proxy",
											},
											Key: "consoleClientID",
										},
									},
								},
								{
									Name: "CLIENT_SECRET",
									ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "auth-proxy",
											},
											Key: "consoleSecret",
										},
									},
								},
								{
									Name: "OIDC_ISSUER_URL",
									ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "auth-proxy",
											},
											Key: "issuer",
										},
									},
								},
								{
									Name: "COOKIE_SECRET",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "auth-proxy",
											},
											Key: "cookieSecret",
										},
									},
								},
							},
							Args: []string{
								"--provider=oidc",
								"--scope=openid profile email groups",
								"--client-id=$(CLIENT_ID)",
								"--client-secret=$(CLIENT_SECRET)",
								"--cookie-secret=$(COOKIE_SECRET)",
								"--proxy-prefix=/auth",
								"--pass-host-header=true",
								"--pass-user-headers=true",
								"--pass-basic-auth=false",
								"--cookie-name=_oauth2_proxy_" + ingress.GetName() + "_auth",
								"--cookie-domain=" + host,
								"--cookie-expire=" + cookieExpire,
								"--cookie-secure=" + secure,
								"--cookie-httponly=true",
								"--session-cookie-minimal=true",
								"--email-domain=" + emailDomain,
								"--redirect-url=" + protocol + "://" + host + "/auth/callback",
								"--oidc-issuer-url=$(OIDC_ISSUER_URL)",
								"--http-address=http://0.0.0.0:4180",
								"--upstream=http://" + service + "." + ingress.GetNamespace() + ".svc:" + servicePort,
								"--skip-auth-regex=^/.well-known/acme-challenge/",
								"--skip-provider-button=true",
								"--ssl-insecure-skip-verify",
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									Protocol:      corev1.ProtocolTCP,
									ContainerPort: 4180,
								},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path:   "/ping",
										Port:   intstr.FromInt(port),
										Scheme: corev1.URISchemeHTTP,
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
								TimeoutSeconds:      1,
								SuccessThreshold:    1,
								FailureThreshold:    3,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path:   "/ping",
										Port:   intstr.FromInt(port),
										Scheme: corev1.URISchemeHTTP,
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
								TimeoutSeconds:      1,
								SuccessThreshold:    1,
								FailureThreshold:    3,
							},
							TerminationMessagePath:   "/dev/termination-log",
							TerminationMessagePolicy: corev1.TerminationMessageReadFile,
							ImagePullPolicy:          corev1.PullIfNotPresent,
						},
					},
				},
			},
		},
	}
	return deployment

}

func createService(ingress metav1.Object) *corev1.Service {

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ingress.GetName() + "-auth-svc",
			Namespace: ingress.GetNamespace(),
			Labels: map[string]string{
				"k8s-app": ingress.GetName() + "-auth",
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"k8s-app": ingress.GetName() + "-auth",
			},
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Protocol:   corev1.ProtocolTCP,
					Port:       4180,
					TargetPort: intstr.FromString("http"),
				},
			},
		},
	}
	return service
}

func createConfigMap(ingress metav1.Object, dexConfigMap *corev1.ConfigMap) *corev1.ConfigMap {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "auth-proxy",
			Namespace: ingress.GetNamespace(),
		},
		Data: map[string]string{
			"consoleClientID": dexConfigMap.Data["consoleClientID"],
			"consoleSecret":   dexConfigMap.Data["consoleSecret"],
			"issuer":          dexConfigMap.Data["issuer"],
		},
	}

	return configMap
}

func createSecret(ingress metav1.Object) (*corev1.Secret, error) {
	_, cookie, err := random(16)
	if err != nil {
		return nil, err
	}
	std := base64.StdEncoding
	encoded := make([]byte, std.EncodedLen(len(cookie)))
	std.Encode(encoded, cookie)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "auth-proxy",
			Namespace: ingress.GetNamespace(),
		},
		Data: map[string][]byte{
			"cookieSecret": encoded,
		},
		Type: "Opaque",
	}

	return secret, nil
}

func random(randomBytesLen int) (string, []byte, error) {
	buf := make([]byte, randomBytesLen)
	read, err := rand.Read(buf)
	if err != nil {
		return "", nil, fmt.Errorf("Unable to generate random chunk: random read error (read %d of %d random bytes): %v",
			read, randomBytesLen, err)
	}
	return base64.RawStdEncoding.EncodeToString(buf), buf, nil
}

func int32Ptr(i int32) *int32 { return &i }
