/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2018 Red Hat, Inc.
 *
 */

package noderecovery

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubeinformers "k8s.io/client-go/informers"
	kubefake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"

	clusterapiv1alpha1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
	clusterapiinformers "sigs.k8s.io/cluster-api/pkg/client/informers_generated/externalversions"

	"kubevirt.io/node-recovery/pkg/apis/noderecovery/v1alpha1"
	"kubevirt.io/node-recovery/pkg/client/clientset/versioned/fake"
	informers "kubevirt.io/node-recovery/pkg/client/informers/externalversions"
	"kubevirt.io/node-recovery/pkg/controller"
	testutils "kubevirt.io/node-recovery/pkg/testing"
	clusterapifake "kubevirt.io/node-recovery/pkg/testing/cluster-api/client/clientset/fake"
)

var (
	alwaysReady = func() bool { return true }
	noTimestamp = metav1.Time{}
)

const remediationConditionsData = `items:
- name: Ready 
  timeout: 60s
  status: Unknown`

const unrelatedConditionData = `items:
- name: Unrelated 
  timeout: 60s
  status: Unknown`

const unrelatedStatusData = `items:
- name: Ready 
  timeout: 60s
  status: Unrelated`

func newMachine(name string) *clusterapiv1alpha1.Machine {
	return &clusterapiv1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: v1alpha1.NamespaceNoderecovery,
		},
		TypeMeta: metav1.TypeMeta{
			Kind: "Machine",
		},
		Spec: clusterapiv1alpha1.MachineSpec{},
	}
}

func newNode(name string, ready bool) *corev1.Node {
	conditionReady := corev1.ConditionTrue
	if !ready {
		conditionReady = corev1.ConditionUnknown
	}

	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceNone,
			Annotations: map[string]string{
				"machine": fmt.Sprintf("%s/%s", v1alpha1.NamespaceNoderecovery, name),
			},
		},
		TypeMeta: metav1.TypeMeta{
			Kind: "Node",
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: conditionReady,
				},
			},
		},
	}
}

func newNodeRemediation(name string, phase v1alpha1.NodeRemediationPhase, startTime metav1.Time) *v1alpha1.NodeRemediation {
	return &v1alpha1.NodeRemediation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceNone,
		},
		TypeMeta: metav1.TypeMeta{
			Kind: "NodeRemediation",
		},
		Spec: &v1alpha1.NodeRemediationSpec{
			NodeName: name,
		},
		Status: &v1alpha1.NodeRemediationStatus{
			Phase:     phase,
			StartTime: startTime,
		},
	}
}

func newConfigMap(name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: v1alpha1.NamespaceNoderecovery,
		},
		TypeMeta: metav1.TypeMeta{
			Kind: "ConfigMap",
		},
		Data: data,
	}
}

type fixture struct {
	t *testing.T

	// clients
	kubeclient       *kubefake.Clientset
	clusterapiclient *clusterapifake.Clientset
	client           *fake.Clientset

	// informers
	kubeInformerFactory       kubeinformers.SharedInformerFactory
	informerFactory           informers.SharedInformerFactory
	clusterapiInformerFactory clusterapiinformers.SharedInformerFactory

	// Objects to put in the store.
	machineLister         []*clusterapiv1alpha1.Machine
	nodeLister            []*corev1.Node
	configMapLister       []*corev1.ConfigMap
	nodeRemediationLister []*v1alpha1.NodeRemediation

	// Actions expected to happen on the client. Objects from here are also
	// preloaded into NewSimpleFake.
	actions []core.Action

	kubeObjects       []runtime.Object
	objects           []runtime.Object
	clusterapiObjects []runtime.Object

	recorder *record.FakeRecorder
}

func (f *fixture) expectCreateMachineAction(machine *clusterapiv1alpha1.Machine) {
	f.actions = append(f.actions, core.NewCreateAction(schema.GroupVersionResource{Resource: "machines"}, machine.Namespace, machine))
}

func (f *fixture) expectDeleteMachineAction(machine *clusterapiv1alpha1.Machine) {
	f.actions = append(f.actions, core.NewDeleteAction(schema.GroupVersionResource{Resource: "machines"}, machine.Namespace, machine.Name))
}

func (f *fixture) expectCreateNodeRemediationAction(nr *v1alpha1.NodeRemediation) {
	f.actions = append(f.actions, core.NewCreateAction(schema.GroupVersionResource{Resource: "noderemediations"}, nr.Namespace, nr))
}

func (f *fixture) expectDeleteNodeRemediationAction(nr *v1alpha1.NodeRemediation) {
	f.actions = append(f.actions, core.NewDeleteAction(schema.GroupVersionResource{Resource: "noderemediations"}, nr.Namespace, nr.Name))
}

func (f *fixture) expectUpdateNodeRemediationAction(nr *v1alpha1.NodeRemediation) {
	f.actions = append(f.actions, core.NewUpdateAction(schema.GroupVersionResource{Resource: "noderemediations"}, nr.Namespace, nr))
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.kubeObjects = []runtime.Object{}
	f.objects = []runtime.Object{}
	f.clusterapiObjects = []runtime.Object{}
	return f
}

func (f *fixture) newController() *NodeRecoveryController {
	f.kubeclient = kubefake.NewSimpleClientset(f.kubeObjects...)
	f.kubeInformerFactory = kubeinformers.NewSharedInformerFactory(f.kubeclient, controller.NoResyncPeriodFunc())

	f.client = fake.NewSimpleClientset(f.objects...)
	f.informerFactory = informers.NewSharedInformerFactory(f.client, controller.NoResyncPeriodFunc())

	f.clusterapiclient = clusterapifake.NewSimpleClientset(f.clusterapiObjects...)
	f.clusterapiInformerFactory = clusterapiinformers.NewSharedInformerFactory(f.clusterapiclient, controller.NoResyncPeriodFunc())

	f.recorder = record.NewFakeRecorder(50)

	c := NewNodeRecoveryController(
		f.kubeclient,
		f.client,
		f.clusterapiclient,
		f.kubeInformerFactory.Core().V1().Nodes(),
		f.kubeInformerFactory.Core().V1().ConfigMaps(),
		f.informerFactory.Noderecovery().V1alpha1().NodeRemediations(),
		f.clusterapiInformerFactory.Cluster().V1alpha1().Machines(),
	)

	c.recorder = f.recorder
	c.machineSynced = alwaysReady
	c.nodeSynced = alwaysReady
	c.configMapSynced = alwaysReady
	c.nodeRemediationSynced = alwaysReady

	for _, m := range f.machineLister {
		f.clusterapiInformerFactory.Cluster().V1alpha1().Machines().Informer().GetIndexer().Add(m)
	}
	for _, n := range f.nodeLister {
		f.kubeInformerFactory.Core().V1().Nodes().Informer().GetIndexer().Add(n)
	}
	for _, nr := range f.nodeRemediationLister {
		f.informerFactory.Noderecovery().V1alpha1().NodeRemediations().Informer().GetIndexer().Add(nr)
	}
	for _, cm := range f.configMapLister {
		f.kubeInformerFactory.Core().V1().ConfigMaps().Informer().GetIndexer().Add(cm)
	}
	return c
}

func (f *fixture) runExpectError(nodeName string, startInformers bool) {
	f.run_(nodeName, startInformers, true)
}

func (f *fixture) run(nodeName string) {
	f.run_(nodeName, true, false)
}

func (f *fixture) run_(nodeName string, startInformers bool, expectError bool) {
	c := f.newController()
	if startInformers {
		stopCh := make(chan struct{})
		defer close(stopCh)
		f.kubeInformerFactory.Start(stopCh)
		f.informerFactory.Start(stopCh)
		f.clusterapiInformerFactory.Start(stopCh)
	}

	err := c.sync(nodeName)
	if !expectError && err != nil {
		f.t.Errorf("error syncing deployment: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing deployment, got nil")
	}
	actions := filterInformerActions(f.client.Actions())
	for _, machineAction := range filterInformerActions(f.clusterapiclient.Actions()) {
		actions = append(actions, machineAction)
	}
	for i, action := range actions {
		if len(f.actions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(actions)-len(f.actions), actions[i:])
			break
		}

		expectedAction := f.actions[i]
		if !(expectedAction.Matches(action.GetVerb(), action.GetResource().Resource) && action.GetSubresource() == expectedAction.GetSubresource()) {
			f.t.Errorf("Expected\n\t%#v\ngot\n\t%#v", expectedAction, action)
			continue
		}
	}

	if len(f.actions) > len(actions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.actions)-len(actions), f.actions[len(actions):])
	}
}

func filterInformerActions(actions []core.Action) []core.Action {
	ret := []core.Action{}
	for _, action := range actions {
		if len(action.GetNamespace()) == 0 &&
			(action.Matches("list", "nodes") ||
				action.Matches("list", "configmaps") ||
				action.Matches("list", "noderemediations") ||
				action.Matches("list", "machines") ||
				action.Matches("watch", "nodes") ||
				action.Matches("list", "configmaps") ||
				action.Matches("watch", "noderemediations") ||
				action.Matches("watch", "machines")) {
			continue
		}
		ret = append(ret, action)
	}

	return ret
}

func TestSyncWithReadyNodeDoesNotCreateNodeRemediation(t *testing.T) {
	f := newFixture(t)

	n := newNode("ready-node", true)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	cm := newConfigMap(v1alpha1.ConfigMapRemediationConditions, map[string]string{
		"conditions": remediationConditionsData,
	})
	f.configMapLister = append(f.configMapLister, cm)
	f.kubeObjects = append(f.kubeObjects, cm)

	f.run(testutils.GetKey(n, t))
}

func TestSyncWithNotReadyNodeCreatesNodeRemediation(t *testing.T) {
	f := newFixture(t)

	n := newNode("notready-node", false)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	cm := newConfigMap(v1alpha1.ConfigMapRemediationConditions, map[string]string{
		"conditions": remediationConditionsData,
	})
	f.configMapLister = append(f.configMapLister, cm)
	f.kubeObjects = append(f.kubeObjects, cm)
	
	nr := newNodeRemediation("notready-node", v1alpha1.NodeRemediationPhaseInit, noTimestamp)

	f.expectCreateNodeRemediationAction(nr)

	// Check for expected actions
	f.run(testutils.GetKey(n, t))
	// Check for expected events
	testutils.ExpectEvent(f.recorder, "Succeeded to create NodeRemediation", t)
}

func TestSyncWithReadyNodeDeletesNodeRemediationInInitPhase(t *testing.T) {
	deletionOfNodeRemediation(v1alpha1.NodeRemediationPhaseInit, t)
}

func TestSyncWithReadyNodeDeletesNodeRemediationInWaitPhase(t *testing.T) {
	deletionOfNodeRemediation(v1alpha1.NodeRemediationPhaseWait, t)
}

func deletionOfNodeRemediation(phase v1alpha1.NodeRemediationPhase, t *testing.T) {
	f := newFixture(t)

	n := newNode("ready-node", true)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	cm := newConfigMap(v1alpha1.ConfigMapRemediationConditions, map[string]string{
		"conditions": remediationConditionsData,
	})
	f.configMapLister = append(f.configMapLister, cm)
	f.kubeObjects = append(f.kubeObjects, cm)

	nr := newNodeRemediation("ready-node", phase, noTimestamp)
	f.nodeRemediationLister = append(f.nodeRemediationLister, nr)
	f.objects = append(f.objects, nr)

	f.expectDeleteNodeRemediationAction(nr)

	// Check for expected actions
	f.run(testutils.GetKey(n, t))
	// Check for expected events
	testutils.ExpectEvent(f.recorder, "Succeeded to delete NodeRemediation", t)
}

func TestSyncWithNotReadyNodeMoveNodeRemediationToWaitPhase(t *testing.T) {
	f := newFixture(t)

	n := newNode("notready-node", false)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	cm := newConfigMap(v1alpha1.ConfigMapRemediationConditions, map[string]string{
		"conditions": remediationConditionsData,
	})
	f.configMapLister = append(f.configMapLister, cm)
	f.kubeObjects = append(f.kubeObjects, cm)

	nr := newNodeRemediation("notready-node", v1alpha1.NodeRemediationPhaseInit, noTimestamp)
	f.nodeRemediationLister = append(f.nodeRemediationLister, nr)
	f.objects = append(f.objects, nr)

	f.expectUpdateNodeRemediationAction(nr)

	// Check for expected actions
	f.run(testutils.GetKey(n, t))

	// Check for expected events
	testutils.ExpectEvent(f.recorder, "Succeeded to update NodeRemediation phase to Wait", t)
}

func TestSyncWithNotReadyNodeStayInWaitPhaseSpecifiedTime(t *testing.T) {
	f := newFixture(t)

	n := newNode("notready-node", false)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	cm := newConfigMap(v1alpha1.ConfigMapRemediationConditions, map[string]string{
		"conditions": remediationConditionsData,
	})
	f.configMapLister = append(f.configMapLister, cm)
	f.kubeObjects = append(f.kubeObjects, cm)

	nr := newNodeRemediation("notready-node", v1alpha1.NodeRemediationPhaseWait, metav1.Time{
		Time: time.Now(),
	})
	f.nodeRemediationLister = append(f.nodeRemediationLister, nr)
	f.objects = append(f.objects, nr)

	// Check for expected actions
	f.run(testutils.GetKey(n, t))
}

func TestSyncWithNotReadyNodeInWaitPhaseMoveToRemediatePhase(t *testing.T) {
	f := newFixture(t)

	n := newNode("notready-node", false)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	cm := newConfigMap(v1alpha1.ConfigMapRemediationConditions, map[string]string{
		"conditions": remediationConditionsData,
	})
	f.configMapLister = append(f.configMapLister, cm)
	f.kubeObjects = append(f.kubeObjects, cm)

	nr := newNodeRemediation("notready-node", v1alpha1.NodeRemediationPhaseWait, metav1.Time{
		Time: time.Now().Add(-time.Minute),
	})
	f.nodeRemediationLister = append(f.nodeRemediationLister, nr)
	f.objects = append(f.objects, nr)

	machine := newMachine("notready-node")
	f.machineLister = append(f.machineLister, machine)
	f.clusterapiObjects = append(f.clusterapiObjects, machine)

	f.expectUpdateNodeRemediationAction(nr)
	f.expectDeleteMachineAction(machine)
	// Check for expected actions
	f.run(testutils.GetKey(n, t))

	// Check for expected events
	testutils.ExpectEvent(f.recorder, "Succeeded to delete machine object", t)
	testutils.ExpectEvent(f.recorder, "Succeeded to update NodeRemediation phase to Remediate", t)
}

func TestSyncWithNotReadyNodeInRemediatePhaseCreatesMachines(t *testing.T) {
	f := newFixture(t)

	n := newNode("notready-node", false)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	cm := newConfigMap(v1alpha1.ConfigMapRemediationConditions, map[string]string{
		"conditions": remediationConditionsData,
	})
	f.configMapLister = append(f.configMapLister, cm)
	f.kubeObjects = append(f.kubeObjects, cm)

	nr := newNodeRemediation("notready-node", v1alpha1.NodeRemediationPhaseRemediate,
		metav1.Time{Time: time.Now()},
	)
	f.nodeRemediationLister = append(f.nodeRemediationLister, nr)
	f.objects = append(f.objects, nr)

	machine := newMachine("notready-node")

	f.expectCreateMachineAction(machine)
	// Check for expected actions
	f.run(testutils.GetKey(n, t))

	// Check for expected events
	testutils.ExpectEvent(f.recorder, "Succeeded to create machine object", t)
}

func TestSyncWithReadyNodeInRemediatePhaseSucceeds(t *testing.T) {
	f := newFixture(t)

	n := newNode("ready-node", true)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	cm := newConfigMap(v1alpha1.ConfigMapRemediationConditions, map[string]string{
		"conditions": remediationConditionsData,
	})
	f.configMapLister = append(f.configMapLister, cm)
	f.kubeObjects = append(f.kubeObjects, cm)

	nr := newNodeRemediation("ready-node", v1alpha1.NodeRemediationPhaseRemediate,noTimestamp)
	f.nodeRemediationLister = append(f.nodeRemediationLister, nr)
	f.objects = append(f.objects, nr)

	machine := newMachine("ready-node")
	f.machineLister = append(f.machineLister, machine)
	f.clusterapiObjects = append(f.clusterapiObjects, machine)

	f.expectDeleteNodeRemediationAction(nr)
	// Check for expected actions
	f.run(testutils.GetKey(n, t))

	// Check for expected events
	testutils.ExpectEvent(f.recorder, "Succeeded to remediate the node", t)
}
func TestSyncWithNotReadyNodeInRemediatePhaseFailsRemediationAfterTimeout(t *testing.T) {
	f := newFixture(t)
	defaultRemediateTimeout := time.Minute*5

	n := newNode("notready-node", false)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	cm := newConfigMap(v1alpha1.ConfigMapRemediationConditions, map[string]string{
		"conditions": remediationConditionsData,
	})
	f.configMapLister = append(f.configMapLister, cm)
	f.kubeObjects = append(f.kubeObjects, cm)

	nr := newNodeRemediation("notready-node", v1alpha1.NodeRemediationPhaseRemediate,
		metav1.Time{Time: time.Now().Add(-defaultRemediateTimeout)},
	)
	f.nodeRemediationLister = append(f.nodeRemediationLister, nr)
	f.objects = append(f.objects, nr)

	machine := newMachine("notready-node")
	f.machineLister = append(f.machineLister, machine)
	f.clusterapiObjects = append(f.clusterapiObjects, machine)

	f.expectDeleteNodeRemediationAction(nr)
	// Check for expected actions
	f.run(testutils.GetKey(n, t))

	// Check for expected events
	testutils.ExpectEvent(f.recorder, "Failed to remediate the node", t)
}

func TestSyncWithoutRemediationConditionsConfigMap(t *testing.T) {
	f := newFixture(t)

	n := newNode("ready-node", false)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	f.run(testutils.GetKey(n, t))
}

func testBadData(t *testing.T, data map[string]string) {
	f := newFixture(t)

	n := newNode("ready-node", false)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	cm := newConfigMap(v1alpha1.ConfigMapRemediationConditions, data)
	f.configMapLister = append(f.configMapLister, cm)
	f.kubeObjects = append(f.kubeObjects, cm)

	f.runExpectError(testutils.GetKey(n, t), true)
}

func TestSyncWithRemediationConditionsConfigMapWithoutConditionsKey(t *testing.T) {
	testBadData(t, map[string]string{"test": remediationConditionsData})
}

func TestSyncWithRemediationConditionsConfigMapWithBadData(t *testing.T) {
	testBadData(t, map[string]string{"conditions": "bad-data"})
}

func testUnrelatedData(t *testing.T, data map[string]string) {
	f := newFixture(t)

	n := newNode("ready-node", false)
	f.nodeLister = append(f.nodeLister, n)
	f.kubeObjects = append(f.kubeObjects, n)

	cm := newConfigMap(v1alpha1.ConfigMapRemediationConditions, data)
	f.configMapLister = append(f.configMapLister, cm)
	f.kubeObjects = append(f.kubeObjects, cm)

	f.run(testutils.GetKey(n, t))
}

func TestSyncWithRemediationConditionsConfigMapWithUnrelatedCondition(t *testing.T) {
	testUnrelatedData(t, map[string]string{"conditions": unrelatedConditionData})
}

func TestSyncWithRemediationConditionsConfigMapWithUnrelatedConditionStatus(t *testing.T) {
	testUnrelatedData(t, map[string]string{"conditions": unrelatedStatusData})
}
