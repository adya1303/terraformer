// Copyright 2019 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ibm

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sync"

	"github.com/GoogleCloudPlatform/terraformer/terraformutils"
	"github.com/IBM/go-sdk-core/v4/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
)

// InstanceGroupGenerator ...
type InstanceGroupGenerator struct {
	IBMService
	fatalErrors chan error
}

func (g *InstanceGroupGenerator) loadInstanceGroup(instanceGroupID, instanceGroupName string) terraformutils.Resource {
	resources := terraformutils.NewSimpleResource(
		instanceGroupID,
		instanceGroupName,
		"ibm_is_instance_group",
		"ibm",
		[]string{})
	return resources
}

func (g *InstanceGroupGenerator) loadInstanceGroupManager(instanceGroupID, instanceGroupManagerID, managerName string, dependsOn []string) terraformutils.Resource {
	if managerName == "" {
		managerName = fmt.Sprintf("manager-%d-%d", rand.Intn(100), rand.Intn(50))
	}
	resources := terraformutils.NewResource(
		fmt.Sprintf("%s/%s", instanceGroupID, instanceGroupManagerID),
		managerName,
		"ibm_is_instance_group_manager",
		"ibm",
		map[string]string{},
		[]string{},
		map[string]interface{}{
			"depends_on": dependsOn,
		})
	return resources
}

func (g *InstanceGroupGenerator) loadInstanceGroupManagerPolicy(instanceGroupID, instanceGroupManagerID, policyID, policyName string, dependsOn []string) terraformutils.Resource {
	if policyName == "" {
		policyName = fmt.Sprintf("manager-%d-%d", rand.Intn(100), rand.Intn(50))
	}
	resources := terraformutils.NewResource(
		fmt.Sprintf("%s/%s/%s", instanceGroupID, instanceGroupManagerID, policyID),
		policyName,
		"ibm_is_instance_group_manager_policy",
		"ibm",
		map[string]string{},
		[]string{},
		map[string]interface{}{
			"depends_on": dependsOn,
		})
	return resources
}

func (g *InstanceGroupGenerator) handlePolicies(sess *vpcv1.VpcV1, instanceGroupID, instanceGroupManagerID string, policies, dependsOn []string, waitGroup *sync.WaitGroup) {
	defer waitGroup.Done()
	for _, instanceGroupManagerPolicyID := range policies {
		getInstanceGroupManagerPolicyOptions := vpcv1.GetInstanceGroupManagerPolicyOptions{
			ID:                     &instanceGroupManagerPolicyID,
			InstanceGroupID:        &instanceGroupID,
			InstanceGroupManagerID: &instanceGroupManagerID,
		}
		data, response, err := sess.GetInstanceGroupManagerPolicy(&getInstanceGroupManagerPolicyOptions)
		if err != nil {
			g.fatalErrors <- fmt.Errorf("Error Getting InstanceGroup Manager Policy: %s\n%s", err, response)
		}
		instanceGroupManagerPolicy := data.(*vpcv1.InstanceGroupManagerPolicy)
		g.Resources = append(g.Resources, g.loadInstanceGroupManagerPolicy(instanceGroupID,
			instanceGroupManagerID,
			instanceGroupManagerPolicyID,
			*instanceGroupManagerPolicy.Name,
			dependsOn))
	}
}

func (g *InstanceGroupGenerator) handleManagers(sess *vpcv1.VpcV1, instanceGroupID string, managers, dependsOn []string, waitGroup *sync.WaitGroup) {
	defer waitGroup.Done()
	var policiesWG sync.WaitGroup
	for _, instanceGroupManagerID := range managers {
		getInstanceGroupManagerOptions := vpcv1.GetInstanceGroupManagerOptions{
			ID:              &instanceGroupManagerID,
			InstanceGroupID: &instanceGroupID,
		}
		instanceGroupManager, response, err := sess.GetInstanceGroupManager(&getInstanceGroupManagerOptions)
		if err != nil {
			g.fatalErrors <- fmt.Errorf("Error Getting InstanceGroup Manager: %s\n%s", err, response)
		}

		instanceGroupManagerObj := vpcv1.InstanceGroupManager{}
		result, err := json.Marshal(instanceGroupManager)
		if err != nil {
			fmt.Println(err.Error())
		}

		err = json.Unmarshal(result, &instanceGroupManagerObj)
		if err != nil {
			fmt.Println(err.Error())
		}

		g.Resources = append(g.Resources, g.loadInstanceGroupManager(instanceGroupID, instanceGroupManagerID, *instanceGroupManagerObj.Name, dependsOn))

		policies := make([]string, 0)
		for i := 0; i < len(instanceGroupManagerObj.Policies); i++ {
			policies = append(policies, *(instanceGroupManagerObj.Policies[i].ID))
		}
		policiesWG.Add(1)
		dependsOn1 := makeDependsOn(dependsOn,
			"ibm_is_instance_group_manager."+terraformutils.TfSanitize(*instanceGroupManagerObj.Name))
		go g.handlePolicies(sess, instanceGroupID, instanceGroupManagerID, policies, dependsOn1, &policiesWG)
	}
	policiesWG.Wait()
}

func (g *InstanceGroupGenerator) handleInstanceGroups(sess *vpcv1.VpcV1, waitGroup *sync.WaitGroup) {
	// Support for pagination
	defer waitGroup.Done()
	start := ""
	var allrecs []vpcv1.InstanceGroup
	for {
		listInstanceGroupOptions := vpcv1.ListInstanceGroupsOptions{}
		if start != "" {
			listInstanceGroupOptions.Start = &start
		}
		instanceGroupsCollection, response, err := sess.ListInstanceGroups(&listInstanceGroupOptions)
		if err != nil {
			g.fatalErrors <- fmt.Errorf("Error Fetching InstanceGroups %s\n%s", err, response)
		}
		start = GetNext(instanceGroupsCollection.Next)
		allrecs = append(allrecs, instanceGroupsCollection.InstanceGroups...)
		if start == "" {
			break
		}
	}

	var managersWG sync.WaitGroup

	for _, instanceGroup := range allrecs {
		var dependsOn []string
		dependsOn = append(dependsOn,
			"ibm_is_instance_group."+terraformutils.TfSanitize(*instanceGroup.Name))
		instanceGoupID := *instanceGroup.ID
		g.Resources = append(g.Resources, g.loadInstanceGroup(instanceGoupID, *instanceGroup.Name))
		managers := make([]string, 0)
		for i := 0; i < len(instanceGroup.Managers); i++ {
			managers = append(managers, *(instanceGroup.Managers[i].ID))
		}
		managersWG.Add(1)
		go g.handleManagers(sess, instanceGoupID, managers, dependsOn, &managersWG)
	}
	managersWG.Wait()
}

// InitResources ...
func (g *InstanceGroupGenerator) InitResources() error {
	region := g.Args["region"].(string)
	apiKey := os.Getenv("IC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("no API key set")
	}

	isURL := GetVPCEndPoint(region)
	iamURL := GetAuthEndPoint()
	vpcoptions := &vpcv1.VpcV1Options{
		URL: isURL,
		Authenticator: &core.IamAuthenticator{
			ApiKey: apiKey,
			URL:    iamURL,
		},
	}
	sess, err := vpcv1.NewVpcV1(vpcoptions)

	if err != nil {
		return err
	}

	var instanceGroupWG sync.WaitGroup
	instanceGroupWG.Add(1)
	go g.handleInstanceGroups(sess, &instanceGroupWG)

	instanceGroupWG.Wait() //nolint:govet
	return nil
}

func (g *InstanceGroupGenerator) PostConvertHook() error {
	for i, rm := range g.Resources {
		if rm.InstanceInfo.Type != "ibm_is_instance_group_manager" {
			continue
		}
		for _, rg := range g.Resources {
			if rg.InstanceInfo.Type != "ibm_is_instance_group" {
				continue
			}
			if rm.InstanceState.Attributes["instance_group"] == rg.InstanceState.Attributes["id"] {
				g.Resources[i].Item["instance_group"] = "${ibm_is_instance_group." + rg.ResourceName + ".id}"
			}
		}
	}

	for i, rp := range g.Resources {
		if rp.InstanceInfo.Type != "ibm_is_instance_group_manager_policy" {
			continue
		}
		for _, rm := range g.Resources {
			if rm.InstanceInfo.Type != "ibm_is_instance_group_manager" {
				continue
			}
			for _, rg := range g.Resources {
				if rg.InstanceInfo.Type != "ibm_is_instance_group" {
					continue
				}
				if rp.InstanceState.Attributes["instance_group_manager"] == rm.InstanceState.Attributes["id"] {
					g.Resources[i].Item["instance_group_manager"] = "${ibm_is_instance_group_manager." + rm.ResourceName + ".id}"
				}
				if rp.InstanceState.Attributes["instance_group"] == rg.InstanceState.Attributes["id"] {
					g.Resources[i].Item["instance_group"] = "${ibm_is_instance_group." + rg.ResourceName + ".id}"
				}

			}
		}
	}

	return nil
}
