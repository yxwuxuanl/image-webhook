package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"log"
	"net/http"
	"strings"
)

var (
	serverAddr      = flag.String("server-addr", ":443", "")
	tlsKeyFile      = flag.String("tls-key", "./tls.key", "")
	tlsCertFile     = flag.String("tls-cert", "./tls.crt", "")
	registryMirrors = flag.String("registry-mirrors", "", "")
)

const DockerRegistry = "docker.io"

func main() {
	flag.Parse()

	log.Default().SetFlags(log.LstdFlags | log.Lshortfile)

	mirrors := make(map[string]string)
	for _, mirror := range strings.Split(*registryMirrors, ",") {
		items := strings.Split(mirror, ":")
		switch len(items) {
		case 1:
			mirrors[DockerRegistry] = items[0]
		case 2:
			mirrors[items[0]] = items[1]
		}
	}

	// 注册路由
	http.HandleFunc("/validating", noLatestTagHandler)
	http.Handle("/mutating", tagMutatingHandler(mirrors))

	http.HandleFunc("/healthz", func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusNoContent)
	})

	log.Printf("server listening at %s", *serverAddr)

	log.Fatal(http.ListenAndServeTLS(*serverAddr, *tlsCertFile, *tlsKeyFile, nil))
}

func noLatestTagHandler(rw http.ResponseWriter, r *http.Request) {
	// 解析请求数据
	admissionReview, pod, err := decodeAdmissionReview(r.Body)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		log.Println(err.Error())
		return
	}

	// 构建响应数据
	admissionResponse := &admissionv1.AdmissionResponse{
		UID:     admissionReview.Request.UID,
		Allowed: true,
	}

	for _, container := range pod.Spec.Containers {
		if isLatestTag(container.Image) {
			admissionResponse.Allowed = false
			admissionResponse.Warnings = []string{"use `latest` tag is not allowed"}
			break
		}
	}

	err = json.NewEncoder(rw).Encode(admissionv1.AdmissionReview{
		Response: admissionResponse,
		TypeMeta: admissionReview.TypeMeta,
	})

	if err != nil {
		log.Printf("failed to encode admissionReview: %s", err)
	}
}

func tagMutatingHandler(mirrors map[string]string) http.Handler {
	jsonPatch := admissionv1.PatchTypeJSONPatch

	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		admissionReview, pod, err := decodeAdmissionReview(r.Body)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			log.Println(err.Error())
			return
		}

		admissionResponse := &admissionv1.AdmissionResponse{
			UID:     admissionReview.Request.UID,
			Allowed: true,
		}

		defer func() {
			err := json.NewEncoder(rw).Encode(admissionv1.AdmissionReview{
				Response: admissionResponse,
				TypeMeta: admissionReview.TypeMeta,
			})

			if err != nil {
				log.Printf("failed to encode admissionReview: %s", err)
			}
		}()

		if len(mirrors) == 0 {
			return
		}

		var patches []map[string]string

		for i, container := range pod.Spec.Containers {
			image, replaced := replaceImage(container.Image, mirrors)

			if replaced {
				// 构建 JSON Patch 替换指定字段值
				patches = append(patches, map[string]string{
					"op":    "replace",
					"path":  fmt.Sprintf("/spec/containers/%d/image", i),
					"value": image,
				})
			}
		}

		if len(patches) > 0 {
			admissionResponse.PatchType = &jsonPatch
			admissionResponse.Patch, _ = json.Marshal(patches)
		}
	})
}

func isLatestTag(image string) bool {
	return !strings.Contains(image, ":") || strings.HasSuffix(image, ":latest")
}

func replaceImage(image string, mirrors map[string]string) (string, bool) {
	if count := strings.Count(image, "/"); count < 2 {
		if mirror := mirrors[DockerRegistry]; mirror != "" {
			if count == 0 {
				return mirror + "/library/" + image, true
			} else {
				return mirror + "/" + image, true
			}
		} else {
			return image, false
		}
	}

	for fromRegistry, toRegistry := range mirrors {
		if strings.HasPrefix(image, fromRegistry+"/") {
			return strings.Replace(image, fromRegistry, toRegistry, 1), true
		}
	}

	return image, false
}

func decodeAdmissionReview(r io.ReadCloser) (*admissionv1.AdmissionReview, *corev1.Pod, error) {
	defer r.Close()

	admissionReview := new(admissionv1.AdmissionReview)
	if err := json.NewDecoder(r).Decode(admissionReview); err != nil {
		return nil, nil, fmt.Errorf("failed to decode admission review: %w", err)
	}

	pod := new(corev1.Pod)
	if err := json.Unmarshal(admissionReview.Request.Object.Raw, pod); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal request object: %w", err)
	}

	return admissionReview, pod, nil
}
