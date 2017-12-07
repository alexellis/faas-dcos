// Copyright (c) Alex Ellis 2017, Alberto Quario 2017. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

package handlers

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/openfaas/faas/gateway/requests"

	marathon "github.com/gambol99/go-marathon"
)

// initialReplicasCount how many replicas to start of creating for a function
const initialReplicasCount = 1

const functionNamespace string = "default"

// ValidateDeployRequest validates that the service name is valid for Kubernetes
func ValidateDeployRequest(request *requests.CreateFunctionRequest) error {
	// Regex for RFC-1123 validation:
	// 	k8s.io/kubernetes/pkg/util/validation/validation.go
	var validDNS = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	matched := validDNS.MatchString(request.Service)
	if matched {
		return nil
	}

	return fmt.Errorf("(%s) must be a valid DNS entry for service name", request.Service)
}

// MakeDeployHandler creates a handler to create new functions in the cluster
func MakeDeployHandler(client marathon.Marathon) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		body, _ := ioutil.ReadAll(r.Body)

		request := requests.CreateFunctionRequest{}
		err := json.Unmarshal(body, &request)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if err := ValidateDeployRequest(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error()))
			return
		}

		application := createApplicationRequest(request)
		if _, err := client.CreateApplication(application); err != nil {
			reportError(w, err, application)
			return
		}

		if err = client.WaitOnApplication(application.ID, 5*time.Second); err != nil {
			reportError(w, err, application)
			return
		}

		log.Println("Created application -" + application.ID)
		log.Println(string(body))

		w.WriteHeader(http.StatusAccepted)
	}
}

// MakeUpdateHandler update specified function
func MakeUpdateHandler(client marathon.Marathon) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		body, _ := ioutil.ReadAll(r.Body)

		request := requests.CreateFunctionRequest{}
		err := json.Unmarshal(body, &request)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		application, findAppErr := client.ApplicationBy(Function2ID(request.Service), nil)

		if findAppErr != nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(findAppErr.Error()))
			return
		}

		if application == nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("Function " + request.Service + " not found"))
			return
		}

		initialReplicas := initialReplicasCount

		if request.Labels != nil {
			if min := getMinReplicaCount(*request.Labels); min != 0 {
				initialReplicas = min
			}
		}

		application.Count(initialReplicas)
		application.Container.Docker.Container(request.Image)

		buildEnvVars(request, application)
		if request.Labels != nil {
			for k, v := range *request.Labels {
				application.AddLabel(k, v)
			}
		}

		if _, err := client.UpdateApplication(application, false); err != nil {
			reportError(w, err, application)
			return
		}
	}
}

/*
readme

* Upgraded to gateway:0.6.13
* Set replica count on create/update from com.openfaas.scale.min label (see https://github.com/openfaas/faas-netes/pull/97)
* Implemented Health handler
* Implemented Update handler

TODO
* Secrets
*/

func reportError(w http.ResponseWriter, err error, application *marathon.Application) {
	log.Printf("Failed to create application: %s, error: %s\n", application, err)
	w.WriteHeader(http.StatusInternalServerError)
	w.Write([]byte(err.Error()))
}

func createApplicationRequest(request requests.CreateFunctionRequest) (application *marathon.Application) {
	initialReplicas := initialReplicasCount

	if request.Labels != nil {
		if min := getMinReplicaCount(*request.Labels); min != 0 {
			initialReplicas = min
		}
	}

	pm := marathon.PortMapping{
		ContainerPort: 8080,
		HostPort:      0,
		ServicePort:   0,
		Protocol:      "tcp"}
	pm.AddLabel("VIP_0", "/"+Function2Endpoint(request.Service)+":8080")

	us := &marathon.UpgradeStrategy{}
	us.SetMinimumHealthCapacity(1.0)
	us.SetMaximumOverCapacity(1.0)

	application = marathon.NewDockerApplication().Name(Function2ID(request.Service)).Count(initialReplicas).CPU(0.1).Memory(128)
	buildEnvVars(request, application)
	application.SetUpgradeStrategy(*us)
	application.AddLabel("faas_function", request.Service)
	if request.Labels != nil {
		for k, v := range *request.Labels {
			application.AddLabel(k, v)
		}
	}
	application.Container.Docker.
		Container(request.Image).
		SetForcePullImage(true).
		Bridged().
		ExposePort(pm)

	log.Println("DEBUG --------------------- function descriptor")
	log.Println(application)
	log.Println("DEBUG --------------------- function descriptor")

	return
}

func buildEnvVars(request requests.CreateFunctionRequest, application *marathon.Application) {
	if len(request.EnvProcess) > 0 {
		application.AddEnv("fprocess", request.EnvProcess)
	}

	for k, v := range request.EnvVars {
		if len(request.EnvProcess) > 0 {
			application.AddEnv(k, v)
		}
	}
}

func getMinReplicaCount(labels map[string]string) int {
	if value, exists := labels["com.openfaas.scale.min"]; exists {
		minReplicas, err := strconv.Atoi(value)
		if err == nil && minReplicas > 0 {
			return minReplicas
		}
		log.Println(err)
	}

	return 0
}
