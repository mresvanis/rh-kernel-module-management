package controllers

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kmmv1beta1 "github.com/rh-ecosystem-edge/kernel-module-management/api/v1beta1"
	"github.com/rh-ecosystem-edge/kernel-module-management/internal/api"
	"github.com/rh-ecosystem-edge/kernel-module-management/internal/build"
	"github.com/rh-ecosystem-edge/kernel-module-management/internal/client"
	"github.com/rh-ecosystem-edge/kernel-module-management/internal/constants"
	"github.com/rh-ecosystem-edge/kernel-module-management/internal/daemonset"
	"github.com/rh-ecosystem-edge/kernel-module-management/internal/metrics"
	"github.com/rh-ecosystem-edge/kernel-module-management/internal/module"
	"github.com/rh-ecosystem-edge/kernel-module-management/internal/sign"
	"github.com/rh-ecosystem-edge/kernel-module-management/internal/statusupdater"
	"github.com/rh-ecosystem-edge/kernel-module-management/internal/utils"
	ocpbuildutils "github.com/rh-ecosystem-edge/kernel-module-management/internal/utils/ocpbuild"
	"go.uber.org/mock/gomock"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	namespace = "namespace"
)

var _ = Describe("ModuleReconciler_Reconcile", func() {
	var (
		ctrl            *gomock.Controller
		mockDC          *daemonset.MockDaemonSetCreator
		mockReconHelper *MockmoduleReconcilerHelperAPI
		mockSU          *statusupdater.MockModuleStatusUpdater
		mr              *ModuleReconciler
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockDC = daemonset.NewMockDaemonSetCreator(ctrl)
		mockReconHelper = NewMockmoduleReconcilerHelperAPI(ctrl)
		mockSU = statusupdater.NewMockModuleStatusUpdater(ctrl)

		mr = &ModuleReconciler{
			daemonAPI:         mockDC,
			reconHelperAPI:    mockReconHelper,
			statusUpdaterAPI:  mockSU,
			operatorNamespace: "different namespace",
		}
	})

	const moduleName = "test-module"

	nsn := types.NamespacedName{
		Name:      moduleName,
		Namespace: namespace,
	}

	req := reconcile.Request{NamespacedName: nsn}

	ctx := context.Background()

	It("should return ok if module has been deleted", func() {
		mockReconHelper.EXPECT().getRequestedModule(ctx, nsn).Return(nil, apierrors.NewNotFound(schema.GroupResource{}, "whatever"))

		res, err := mr.Reconcile(ctx, req)

		Expect(res).To(Equal(reconcile.Result{}))
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("check error flows", func(getModuleError, getNodesError, getMappingsError, getDSError, handleBuildError,
		handleSignError, handleDCError, handlePluginError, gcError bool) {
		mod := kmmv1beta1.Module{}
		selectNodesList := []v1.Node{v1.Node{}}
		kernelNodesList := []v1.Node{v1.Node{}}
		mappings := map[string]*api.ModuleLoaderData{"kernelVersion": &api.ModuleLoaderData{}}
		moduleDS := []appsv1.DaemonSet{appsv1.DaemonSet{}}
		returnedError := fmt.Errorf("some error")
		if getModuleError {
			mockReconHelper.EXPECT().getRequestedModule(ctx, nsn).Return(nil, returnedError)
			goto executeTestFunction
		}
		mockReconHelper.EXPECT().getRequestedModule(ctx, nsn).Return(&mod, nil)
		mockReconHelper.EXPECT().setKMMOMetrics(ctx)
		if getNodesError {
			mockReconHelper.EXPECT().getNodesListBySelector(ctx, &mod).Return(nil, returnedError)
			goto executeTestFunction
		}
		mockReconHelper.EXPECT().getNodesListBySelector(ctx, &mod).Return(selectNodesList, nil)
		if getMappingsError {
			mockReconHelper.EXPECT().getRelevantKernelMappingsAndNodes(ctx, &mod, selectNodesList).Return(nil, nil, returnedError)
			goto executeTestFunction
		}
		mockReconHelper.EXPECT().getRelevantKernelMappingsAndNodes(ctx, &mod, selectNodesList).Return(mappings, kernelNodesList, nil)
		if getDSError {
			mockDC.EXPECT().GetModuleDaemonSets(ctx, mod.Name, mod.Namespace).Return(nil, returnedError)
			goto executeTestFunction
		}
		mockDC.EXPECT().GetModuleDaemonSets(ctx, mod.Name, mod.Namespace).Return(moduleDS, nil)
		if handleBuildError {
			mockReconHelper.EXPECT().handleBuild(ctx, mappings["kernelVersion"]).Return(false, returnedError)
			goto executeTestFunction
		}
		mockReconHelper.EXPECT().handleBuild(ctx, mappings["kernelVersion"]).Return(true, nil)
		if handleSignError {
			mockReconHelper.EXPECT().handleSigning(ctx, mappings["kernelVersion"]).Return(false, returnedError)
			goto executeTestFunction
		}
		mockReconHelper.EXPECT().handleSigning(ctx, mappings["kernelVersion"]).Return(true, nil)
		if handleDCError {
			mockReconHelper.EXPECT().handleDriverContainer(ctx, mappings["kernelVersion"], moduleDS).Return(returnedError)
			goto executeTestFunction
		}
		mockReconHelper.EXPECT().handleDriverContainer(ctx, mappings["kernelVersion"], moduleDS).Return(nil)
		if handlePluginError {
			mockReconHelper.EXPECT().handleDevicePlugin(ctx, &mod, moduleDS).Return(returnedError)
			goto executeTestFunction
		}
		mockReconHelper.EXPECT().handleDevicePlugin(ctx, &mod, moduleDS).Return(nil)
		if gcError {
			mockReconHelper.EXPECT().garbageCollect(ctx, &mod, mappings, moduleDS).Return(returnedError)
			goto executeTestFunction
		}
		mockReconHelper.EXPECT().garbageCollect(ctx, &mod, mappings, moduleDS).Return(nil)
		mockSU.EXPECT().ModuleUpdateStatus(ctx, &mod, kernelNodesList, selectNodesList, moduleDS).Return(returnedError)

	executeTestFunction:
		res, err := mr.Reconcile(ctx, req)

		Expect(res).To(Equal(reconcile.Result{}))
		Expect(err).To(HaveOccurred())

	},
		Entry("getRequestedModule failed", true, false, false, false, false, false, false, false, false),
		Entry("getNodesListBySelector failed", false, true, false, false, false, false, false, false, false),
		Entry("getRelevantKernelMappingsAndNodes failed", false, false, true, false, false, false, false, false, false),
		Entry("ModuleDaemonSetsByKernelVersion failed", false, false, false, true, false, false, false, false, false),
		Entry("handleBuild failed ", false, false, false, false, true, false, false, false, false),
		Entry("handleSig failedn", false, false, false, false, false, true, false, false, false),
		Entry("handleDriverContainer failed", false, false, false, false, false, false, true, false, false),
		Entry("handleDevicePlugin failed", false, false, false, false, false, false, false, true, false),
		Entry("garbageCollect failed", false, false, false, false, false, false, false, false, true),
		Entry("moduleUpdateStatus failed", false, false, false, false, false, false, false, false, false),
	)

	It("Build has not completed successfully", func() {
		mod := kmmv1beta1.Module{}
		selectNodesList := []v1.Node{v1.Node{}}
		kernelNodesList := []v1.Node{v1.Node{}}
		mappings := map[string]*api.ModuleLoaderData{"kernelVersion": &api.ModuleLoaderData{}}
		moduleDS := []appsv1.DaemonSet{appsv1.DaemonSet{}}
		gomock.InOrder(
			mockReconHelper.EXPECT().getRequestedModule(ctx, nsn).Return(&mod, nil),
			mockReconHelper.EXPECT().setKMMOMetrics(ctx),
			mockReconHelper.EXPECT().getNodesListBySelector(ctx, &mod).Return(selectNodesList, nil),
			mockReconHelper.EXPECT().getRelevantKernelMappingsAndNodes(ctx, &mod, selectNodesList).Return(mappings, kernelNodesList, nil),
			mockDC.EXPECT().GetModuleDaemonSets(ctx, mod.Name, mod.Namespace).Return(moduleDS, nil),
			mockReconHelper.EXPECT().handleBuild(ctx, mappings["kernelVersion"]).Return(false, nil),
			mockReconHelper.EXPECT().handleDevicePlugin(ctx, &mod, moduleDS).Return(nil),
			mockReconHelper.EXPECT().garbageCollect(ctx, &mod, mappings, moduleDS).Return(nil),
			mockSU.EXPECT().ModuleUpdateStatus(ctx, &mod, kernelNodesList, selectNodesList, moduleDS).Return(nil),
		)

		res, err := mr.Reconcile(ctx, req)

		Expect(res).To(Equal(reconcile.Result{}))
		Expect(err).NotTo(HaveOccurred())

	})

	It("Signing has not completed successfully", func() {
		mod := kmmv1beta1.Module{}
		selectNodesList := []v1.Node{v1.Node{}}
		kernelNodesList := []v1.Node{v1.Node{}}
		mappings := map[string]*api.ModuleLoaderData{"kernelVersion": &api.ModuleLoaderData{}}
		moduleDS := []appsv1.DaemonSet{appsv1.DaemonSet{}}
		gomock.InOrder(
			mockReconHelper.EXPECT().getRequestedModule(ctx, nsn).Return(&mod, nil),
			mockReconHelper.EXPECT().setKMMOMetrics(ctx),
			mockReconHelper.EXPECT().getNodesListBySelector(ctx, &mod).Return(selectNodesList, nil),
			mockReconHelper.EXPECT().getRelevantKernelMappingsAndNodes(ctx, &mod, selectNodesList).Return(mappings, kernelNodesList, nil),
			mockDC.EXPECT().GetModuleDaemonSets(ctx, mod.Name, mod.Namespace).Return(moduleDS, nil),
			mockReconHelper.EXPECT().handleBuild(ctx, mappings["kernelVersion"]).Return(true, nil),
			mockReconHelper.EXPECT().handleSigning(ctx, mappings["kernelVersion"]).Return(false, nil),
			mockReconHelper.EXPECT().handleDevicePlugin(ctx, &mod, moduleDS).Return(nil),
			mockReconHelper.EXPECT().garbageCollect(ctx, &mod, mappings, moduleDS).Return(nil),
			mockSU.EXPECT().ModuleUpdateStatus(ctx, &mod, kernelNodesList, selectNodesList, moduleDS).Return(nil),
		)

		res, err := mr.Reconcile(ctx, req)

		Expect(res).To(Equal(reconcile.Result{}))
		Expect(err).NotTo(HaveOccurred())
	})

	It("Good flow", func() {
		mod := kmmv1beta1.Module{}
		selectNodesList := []v1.Node{v1.Node{}}
		kernelNodesList := []v1.Node{v1.Node{}}
		mappings := map[string]*api.ModuleLoaderData{"kernelVersion": &api.ModuleLoaderData{}}
		moduleDS := []appsv1.DaemonSet{appsv1.DaemonSet{}}
		gomock.InOrder(
			mockReconHelper.EXPECT().getRequestedModule(ctx, nsn).Return(&mod, nil),
			mockReconHelper.EXPECT().setKMMOMetrics(ctx),
			mockReconHelper.EXPECT().getNodesListBySelector(ctx, &mod).Return(selectNodesList, nil),
			mockReconHelper.EXPECT().getRelevantKernelMappingsAndNodes(ctx, &mod, selectNodesList).Return(mappings, kernelNodesList, nil),
			mockDC.EXPECT().GetModuleDaemonSets(ctx, mod.Name, mod.Namespace).Return(moduleDS, nil),
			mockReconHelper.EXPECT().handleBuild(ctx, mappings["kernelVersion"]).Return(true, nil),
			mockReconHelper.EXPECT().handleSigning(ctx, mappings["kernelVersion"]).Return(true, nil),
			mockReconHelper.EXPECT().handleDriverContainer(ctx, mappings["kernelVersion"], moduleDS).Return(nil),
			mockReconHelper.EXPECT().handleDevicePlugin(ctx, &mod, moduleDS).Return(nil),
			mockReconHelper.EXPECT().garbageCollect(ctx, &mod, mappings, moduleDS).Return(nil),
			mockSU.EXPECT().ModuleUpdateStatus(ctx, &mod, kernelNodesList, selectNodesList, moduleDS).Return(nil),
		)

		res, err := mr.Reconcile(ctx, req)

		Expect(res).To(Equal(reconcile.Result{}))
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("ModuleReconciler_getNodesListBySelector", func() {
	var (
		ctrl *gomock.Controller
		clnt *client.MockClient
		mhr  moduleReconcilerHelperAPI
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		clnt = client.NewMockClient(ctrl)
		mhr = newModuleReconcilerHelper(clnt, nil, nil, nil, nil, nil, "")
	})

	It("list failed", func() {
		clnt.EXPECT().List(context.Background(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("some error"))

		nodes, err := mhr.getNodesListBySelector(context.Background(), &kmmv1beta1.Module{})

		Expect(err).To(HaveOccurred())
		Expect(nodes).To(BeNil())
	})

	It("Return only schedulable nodes", func() {
		node1 := v1.Node{
			Spec: v1.NodeSpec{
				Taints: []v1.Taint{
					v1.Taint{
						Effect: v1.TaintEffectNoSchedule,
					},
				},
			},
		}
		node2 := v1.Node{}
		node3 := v1.Node{
			Spec: v1.NodeSpec{
				Taints: []v1.Taint{
					v1.Taint{
						Effect: v1.TaintEffectPreferNoSchedule,
					},
				},
			},
		}
		clnt.EXPECT().List(context.Background(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ interface{}, list *v1.NodeList, _ ...interface{}) error {
				list.Items = []v1.Node{node1, node2, node3}
				return nil
			},
		)
		nodes, err := mhr.getNodesListBySelector(context.Background(), &kmmv1beta1.Module{})

		Expect(err).NotTo(HaveOccurred())
		Expect(nodes).To(Equal([]v1.Node{node2, node3}))

	})
})

var _ = Describe("ModuleReconciler_getRelevantKernelMappingsAndNodes", func() {
	var (
		ctrl   *gomock.Controller
		mockKM *module.MockKernelMapper
		mhr    moduleReconcilerHelperAPI
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockKM = module.NewMockKernelMapper(ctrl)
		mhr = newModuleReconcilerHelper(nil, nil, nil, nil, mockKM, nil, "")
	})

	node1 := v1.Node{
		Status: v1.NodeStatus{
			NodeInfo: v1.NodeSystemInfo{
				KernelVersion: "kernelVersion1",
			},
		},
	}
	node2 := v1.Node{
		Status: v1.NodeStatus{
			NodeInfo: v1.NodeSystemInfo{
				KernelVersion: "kernelVersion2",
			},
		},
	}
	node3 := v1.Node{
		Status: v1.NodeStatus{
			NodeInfo: v1.NodeSystemInfo{
				KernelVersion: "kernelVersion1",
			},
		},
	}

	mld1 := api.ModuleLoaderData{Name: "name1"}
	mld2 := api.ModuleLoaderData{Name: "name2"}

	It("good flow, all mappings exist", func() {
		nodes := []v1.Node{node1, node2, node3}
		expectedNodes := []v1.Node{node1, node2, node3}
		expectedMappings := map[string]*api.ModuleLoaderData{"kernelVersion1": &mld1, "kernelVersion2": &mld2}
		gomock.InOrder(
			mockKM.EXPECT().GetModuleLoaderDataForKernel(&kmmv1beta1.Module{}, node1.Status.NodeInfo.KernelVersion).Return(&mld1, nil),
			mockKM.EXPECT().GetModuleLoaderDataForKernel(&kmmv1beta1.Module{}, node2.Status.NodeInfo.KernelVersion).Return(&mld2, nil),
		)

		mappings, resNodes, err := mhr.getRelevantKernelMappingsAndNodes(context.Background(), &kmmv1beta1.Module{}, nodes)

		Expect(err).NotTo(HaveOccurred())
		Expect(resNodes).To(Equal(expectedNodes))
		Expect(mappings).To(Equal(expectedMappings))

	})

	It("good flow, one mapping does not exist", func() {
		nodes := []v1.Node{node1, node2, node3}
		expectedNodes := []v1.Node{node1, node3}
		expectedMappings := map[string]*api.ModuleLoaderData{"kernelVersion1": &mld1}
		gomock.InOrder(
			mockKM.EXPECT().GetModuleLoaderDataForKernel(&kmmv1beta1.Module{}, node1.Status.NodeInfo.KernelVersion).Return(&mld1, nil),
			mockKM.EXPECT().GetModuleLoaderDataForKernel(&kmmv1beta1.Module{}, node2.Status.NodeInfo.KernelVersion).Return(nil, fmt.Errorf("some error")),
		)

		mappings, resNodes, err := mhr.getRelevantKernelMappingsAndNodes(context.Background(), &kmmv1beta1.Module{}, nodes)

		Expect(err).NotTo(HaveOccurred())
		Expect(resNodes).To(Equal(expectedNodes))
		Expect(mappings).To(Equal(expectedMappings))

	})

})

var _ = Describe("ModuleReconciler_handleBuild", func() {
	var (
		ctrl        *gomock.Controller
		mockBM      *build.MockManager
		mockMetrics *metrics.MockMetrics
		mhr         moduleReconcilerHelperAPI
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockBM = build.NewMockManager(ctrl)
		mockMetrics = metrics.NewMockMetrics(ctrl)
		mhr = newModuleReconcilerHelper(nil, mockBM, nil, nil, nil, mockMetrics, "")
	})

	const (
		moduleName    = "test-module"
		kernelVersion = "1.2.3"
		imageName     = "test-image"
	)

	It("should do nothing when build is skipped", func() {
		mld := &api.ModuleLoaderData{KernelVersion: kernelVersion}

		gomock.InOrder(
			mockBM.EXPECT().ShouldSync(gomock.Any(), mld).Return(false, nil),
		)

		completed, err := mhr.handleBuild(context.Background(), mld)
		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeTrue())
	})

	It("should record that a job was created when the build sync returns StatusCreated", func() {
		mld := api.ModuleLoaderData{
			Name:           moduleName,
			Namespace:      namespace,
			ContainerImage: imageName,
			Build:          &kmmv1beta1.Build{},
			KernelVersion:  kernelVersion,
		}

		gomock.InOrder(
			mockBM.EXPECT().ShouldSync(gomock.Any(), &mld).Return(true, nil),
			mockBM.EXPECT().Sync(gomock.Any(), &mld, true, mld.Owner).Return(ocpbuildutils.Status(ocpbuildutils.StatusCreated), nil),
		)

		completed, err := mhr.handleBuild(context.Background(), &mld)

		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeFalse())
	})

	It("should record that a job was completed, when the build sync returns StatusCompleted", func() {
		mld := &api.ModuleLoaderData{
			Name:           moduleName,
			Namespace:      namespace,
			ContainerImage: imageName,
			Build:          &kmmv1beta1.Build{},
			Owner:          &kmmv1beta1.Module{},
			KernelVersion:  kernelVersion,
		}
		gomock.InOrder(
			mockBM.EXPECT().ShouldSync(gomock.Any(), mld).Return(true, nil),
			mockBM.EXPECT().Sync(gomock.Any(), mld, true, mld.Owner).Return(ocpbuildutils.Status(ocpbuildutils.StatusCompleted), nil),
		)

		completed, err := mhr.handleBuild(context.Background(), mld)

		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeTrue())
	})
})

var _ = Describe("ModuleReconciler_handleSigning", func() {
	var (
		ctrl        *gomock.Controller
		mockSM      *sign.MockSignManager
		mockMetrics *metrics.MockMetrics
		mhr         moduleReconcilerHelperAPI
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockSM = sign.NewMockSignManager(ctrl)
		mockMetrics = metrics.NewMockMetrics(ctrl)
		mhr = newModuleReconcilerHelper(nil, nil, mockSM, nil, nil, mockMetrics, "")
	})

	const (
		moduleName    = "test-module"
		kernelVersion = "1.2.3"
		imageName     = "test-image"
	)

	It("should do nothing when build is skipped", func() {
		mld := &api.ModuleLoaderData{
			ContainerImage: imageName,
			KernelVersion:  kernelVersion,
		}

		gomock.InOrder(
			mockSM.EXPECT().ShouldSync(gomock.Any(), mld).Return(false, nil),
		)

		completed, err := mhr.handleSigning(context.Background(), mld)

		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeTrue())
	})

	It("should record that a job was created when the sign sync returns StatusCreated", func() {
		mld := api.ModuleLoaderData{
			Name:           moduleName,
			Namespace:      namespace,
			ContainerImage: imageName,
			Sign:           &kmmv1beta1.Sign{},
			KernelVersion:  kernelVersion,
		}

		gomock.InOrder(
			mockSM.EXPECT().ShouldSync(gomock.Any(), &mld).Return(true, nil),
			mockSM.EXPECT().Sync(gomock.Any(), &mld, "", true, mld.Owner).Return(ocpbuildutils.Status(ocpbuildutils.StatusCreated), nil),
		)

		completed, err := mhr.handleSigning(context.Background(), &mld)

		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeFalse())
	})

	It("should record that a job was completed when the sign sync returns StatusCompleted", func() {
		mld := api.ModuleLoaderData{
			Name:           moduleName,
			Namespace:      namespace,
			ContainerImage: imageName,
			Sign:           &kmmv1beta1.Sign{},
			KernelVersion:  kernelVersion,
		}

		gomock.InOrder(
			mockSM.EXPECT().ShouldSync(gomock.Any(), &mld).Return(true, nil),
			mockSM.EXPECT().Sync(gomock.Any(), &mld, "", true, mld.Owner).Return(ocpbuildutils.Status(ocpbuildutils.StatusCompleted), nil),
		)

		completed, err := mhr.handleSigning(context.Background(), &mld)

		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeTrue())
	})

	It("should run sign sync with the previous image as well when module build and sign are specified", func() {
		mld := &api.ModuleLoaderData{
			Name:           moduleName,
			Namespace:      namespace,
			ContainerImage: imageName,
			Sign:           &kmmv1beta1.Sign{},
			Build:          &kmmv1beta1.Build{},
			Owner:          &kmmv1beta1.Module{},
			KernelVersion:  kernelVersion,
		}

		gomock.InOrder(
			mockSM.EXPECT().ShouldSync(gomock.Any(), mld).Return(true, nil),
			mockSM.EXPECT().Sync(gomock.Any(), mld, imageName+":"+namespace+"_"+moduleName+"_kmm_unsigned", true, mld.Owner).
				Return(ocpbuildutils.Status(ocpbuildutils.StatusCompleted), nil),
		)

		completed, err := mhr.handleSigning(context.Background(), mld)

		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeTrue())
	})
})

var _ = Describe("ModuleReconciler_handleDriverContainer", func() {
	var (
		ctrl        *gomock.Controller
		clnt        *client.MockClient
		mockDC      *daemonset.MockDaemonSetCreator
		mockMetrics *metrics.MockMetrics
		mhr         moduleReconcilerHelperAPI
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		clnt = client.NewMockClient(ctrl)
		mockDC = daemonset.NewMockDaemonSetCreator(ctrl)
		mockMetrics = metrics.NewMockMetrics(ctrl)
		mhr = newModuleReconcilerHelper(clnt, nil, nil, mockDC, nil, mockMetrics, "namespace")
	})

	It("new daemonset", func() {
		ctx := context.Background()
		mld := api.ModuleLoaderData{
			Name:          "name",
			Namespace:     "namespace",
			KernelVersion: "kernelVersion1",
			ModuleVersion: "v234.e",
		}
		newDS := &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: mld.Namespace, GenerateName: mld.Name + "-"},
		}
		gomock.InOrder(
			clnt.EXPECT().Get(ctx, gomock.Any(), gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{}, "whatever")),
			mockDC.EXPECT().SetDriverContainerAsDesired(ctx, newDS, &mld, true).Return(nil),
			clnt.EXPECT().Create(ctx, gomock.Any()).Return(nil),
		)

		err := mhr.handleDriverContainer(ctx, &mld, nil)

		Expect(err).NotTo(HaveOccurred())

	})

	It("existing daemonset", func() {
		ctx := context.Background()
		mld := api.ModuleLoaderData{
			Name:          "name",
			Namespace:     "namespace",
			KernelVersion: "kernelVersion1",
			ModuleVersion: "wr4656",
		}
		const name = "some name"
		labels := map[string]string{
			constants.KernelLabel: mld.KernelVersion,
			utils.GetModuleLoaderVersionLabelName(mld.Namespace, mld.Name): mld.ModuleVersion,
		}
		existingDS := appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: mld.Namespace, Name: name, Labels: labels},
		}
		gomock.InOrder(
			clnt.EXPECT().Get(ctx, gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ interface{}, _ interface{}, ds *appsv1.DaemonSet, _ ...ctrlclient.GetOption) error {
					ds.SetName(name)
					ds.SetNamespace(mld.Namespace)
					return nil
				},
			),
			mockDC.EXPECT().SetDriverContainerAsDesired(ctx, &existingDS, &mld, true).Return(nil),
		)

		err := mhr.handleDriverContainer(ctx, &mld, []appsv1.DaemonSet{existingDS})

		Expect(err).NotTo(HaveOccurred())

	})

	It("failure in the SetDriverContainerAsDesired", func() {
		ctx := context.Background()
		mld := api.ModuleLoaderData{
			Name:          "name",
			Namespace:     "namespace",
			KernelVersion: "kernelVersion1",
		}
		gomock.InOrder(
			clnt.EXPECT().Get(ctx, gomock.Any(), gomock.Any()).Return(nil),
			mockDC.EXPECT().SetDriverContainerAsDesired(ctx, gomock.Any(), &mld, true).Return(fmt.Errorf("some error")),
		)

		err := mhr.handleDriverContainer(ctx, &mld, nil)

		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("ModuleReconciler_handleDevicePlugin", func() {
	var (
		ctrl        *gomock.Controller
		clnt        *client.MockClient
		mockDC      *daemonset.MockDaemonSetCreator
		mockMetrics *metrics.MockMetrics
		mhr         moduleReconcilerHelperAPI
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		clnt = client.NewMockClient(ctrl)
		mockDC = daemonset.NewMockDaemonSetCreator(ctrl)
		mockMetrics = metrics.NewMockMetrics(ctrl)
		mhr = newModuleReconcilerHelper(clnt, nil, nil, mockDC, nil, mockMetrics, "namespace")
	})

	It("device plugin not defined", func() {
		ctx := context.Background()
		mod := kmmv1beta1.Module{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "moduleName",
				Namespace: "namespace",
			},
		}

		err := mhr.handleDevicePlugin(ctx, &mod, nil)

		Expect(err).NotTo(HaveOccurred())
	})

	It("new daemonset", func() {
		ctx := context.Background()
		mod := kmmv1beta1.Module{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "moduleName",
				Namespace: "namespace",
			},
			Spec: kmmv1beta1.ModuleSpec{
				DevicePlugin: &kmmv1beta1.DevicePluginSpec{},
			},
		}

		newDS := &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: mod.Namespace, GenerateName: mod.Name + "-device-plugin-"},
		}
		gomock.InOrder(
			clnt.EXPECT().Get(ctx, gomock.Any(), gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{}, "whatever")),
			mockDC.EXPECT().SetDevicePluginAsDesired(ctx, newDS, &mod, true).Return(nil),
			clnt.EXPECT().Create(ctx, gomock.Any()).Return(nil),
		)

		err := mhr.handleDevicePlugin(ctx, &mod, nil)

		Expect(err).NotTo(HaveOccurred())
	})

	It("existing daemonset", func() {
		ctx := context.Background()
		mod := kmmv1beta1.Module{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "moduleName",
				Namespace: "namespace",
			},
			Spec: kmmv1beta1.ModuleSpec{
				DevicePlugin: &kmmv1beta1.DevicePluginSpec{},
			},
		}

		const name = "some name"
		existingDS := appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: mod.Namespace, Name: name},
		}
		gomock.InOrder(
			clnt.EXPECT().Get(ctx, gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ interface{}, _ interface{}, ds *appsv1.DaemonSet, _ ...ctrlclient.GetOption) error {
					ds.SetName(name)
					ds.SetNamespace(mod.Namespace)
					return nil
				},
			),
			mockDC.EXPECT().SetDevicePluginAsDesired(ctx, &existingDS, &mod, true).Return(nil),
		)

		err := mhr.handleDevicePlugin(ctx, &mod, []appsv1.DaemonSet{existingDS})

		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("ModuleReconciler_garbageCollect", func() {
	var (
		ctrl   *gomock.Controller
		mockBM *build.MockManager
		mockSM *sign.MockSignManager
		mockDC *daemonset.MockDaemonSetCreator
		mhr    moduleReconcilerHelperAPI
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockBM = build.NewMockManager(ctrl)
		mockSM = sign.NewMockSignManager(ctrl)
		mockDC = daemonset.NewMockDaemonSetCreator(ctrl)
		mhr = newModuleReconcilerHelper(nil, mockBM, mockSM, mockDC, nil, nil, "")
	})

	mod := &kmmv1beta1.Module{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "moduleName",
			Namespace: "namespace",
		},
	}

	It("good flow", func() {
		mldMappings := map[string]*api.ModuleLoaderData{
			"kernelVersion1": &api.ModuleLoaderData{}, "kernelVersion2": &api.ModuleLoaderData{},
		}
		existingDS := []appsv1.DaemonSet{appsv1.DaemonSet{}, appsv1.DaemonSet{}}
		kernelSet := sets.New("kernelVersion1", "kernelVersion2")
		gomock.InOrder(
			mockDC.EXPECT().GarbageCollect(context.Background(), mod, existingDS, kernelSet).Return(nil, nil),
			mockBM.EXPECT().GarbageCollect(context.Background(), mod.Name, mod.Namespace, mod).Return(nil, nil),
			mockSM.EXPECT().GarbageCollect(context.Background(), mod.Name, mod.Namespace, mod).Return(nil, nil),
		)

		err := mhr.garbageCollect(context.Background(), mod, mldMappings, existingDS)

		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("check error flows", func(dcError, buildError bool) {
		returnedError := fmt.Errorf("some error")
		mldMappings := map[string]*api.ModuleLoaderData{
			"kernelVersion1": &api.ModuleLoaderData{}, "kernelVersion2": &api.ModuleLoaderData{},
		}
		existingDS := []appsv1.DaemonSet{appsv1.DaemonSet{}, appsv1.DaemonSet{}}
		kernelSet := sets.New("kernelVersion1", "kernelVersion2")
		if dcError {
			mockDC.EXPECT().GarbageCollect(context.Background(), mod, existingDS, kernelSet).Return(nil, returnedError)
			goto executeTestFunction
		}
		mockDC.EXPECT().GarbageCollect(context.Background(), mod, existingDS, kernelSet).Return(nil, nil)
		if buildError {
			mockBM.EXPECT().GarbageCollect(context.Background(), mod.Name, mod.Namespace, mod).Return(nil, returnedError)
			goto executeTestFunction
		}
		mockBM.EXPECT().GarbageCollect(context.Background(), mod.Name, mod.Namespace, mod).Return(nil, nil)
		mockSM.EXPECT().GarbageCollect(context.Background(), mod.Name, mod.Namespace, mod).Return(nil, returnedError)
	executeTestFunction:
		err := mhr.garbageCollect(context.Background(), mod, mldMappings, existingDS)

		Expect(err).To(HaveOccurred())
	},
		Entry("damoenset GC failed", true, false),
		Entry("build GC failed", false, true),
		Entry("sign GC failed", false, false),
	)

})

var _ = Describe("ModuleReconciler_setKMMOMetrics", func() {
	var (
		ctrl        *gomock.Controller
		clnt        *client.MockClient
		mockMetrics *metrics.MockMetrics
		mhr         moduleReconcilerHelperAPI
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		clnt = client.NewMockClient(ctrl)
		mockMetrics = metrics.NewMockMetrics(ctrl)
		mhr = newModuleReconcilerHelper(clnt, nil, nil, nil, nil, mockMetrics, "")
	})

	ctx := context.Background()

	It("failed to list Modules", func() {
		clnt.EXPECT().List(ctx, gomock.Any(), gomock.Any()).Return(fmt.Errorf("some error"))

		mhr.setKMMOMetrics(ctx)
	})

	DescribeTable("getting metrics data", func(buildInContainer, buildInKM, signInContainer, signInKM, devicePlugin bool, modprobeArg, modprobeRawArg []string) {
		km := kmmv1beta1.KernelMapping{}
		mod1 := kmmv1beta1.Module{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "moduleName",
				Namespace: "namespace",
			},
		}
		mod2 := kmmv1beta1.Module{}
		mod3 := kmmv1beta1.Module{}
		numBuild := 0
		numSign := 0
		numDevicePlugin := 0
		if buildInContainer {
			mod1.Spec.ModuleLoader.Container.Build = &kmmv1beta1.Build{}
			numBuild = 1
		}
		if buildInKM {
			km.Build = &kmmv1beta1.Build{}
			numBuild = 1
		}
		if signInContainer {
			mod1.Spec.ModuleLoader.Container.Sign = &kmmv1beta1.Sign{}
			numSign = 1
		}
		if signInKM {
			km.Sign = &kmmv1beta1.Sign{}
			numSign = 1
		}
		if devicePlugin {
			mod1.Spec.DevicePlugin = &kmmv1beta1.DevicePluginSpec{}
			numDevicePlugin = 1
		}
		if modprobeArg != nil {
			mod1.Spec.ModuleLoader.Container.Modprobe.Args = &kmmv1beta1.ModprobeArgs{Load: modprobeArg}
		}
		if modprobeRawArg != nil {
			mod1.Spec.ModuleLoader.Container.Modprobe.RawArgs = &kmmv1beta1.ModprobeArgs{Load: modprobeRawArg}
		}
		mod1.Spec.ModuleLoader.Container.KernelMappings = []kmmv1beta1.KernelMapping{km}

		clnt.EXPECT().List(context.Background(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ interface{}, list *kmmv1beta1.ModuleList, _ ...interface{}) error {
				list.Items = []kmmv1beta1.Module{mod1, mod2, mod3}
				return nil
			},
		)

		if modprobeArg != nil {
			mockMetrics.EXPECT().SetKMMModprobeArgs(mod1.Name, mod1.Namespace, strings.Join(modprobeArg, ","))
		}
		if modprobeRawArg != nil {
			mockMetrics.EXPECT().SetKMMModprobeRawArgs(mod1.Name, mod1.Namespace, strings.Join(modprobeRawArg, ","))
		}

		mockMetrics.EXPECT().SetKMMModulesNum(3)
		mockMetrics.EXPECT().SetKMMInClusterBuildNum(numBuild)
		mockMetrics.EXPECT().SetKMMInClusterSignNum(numSign)
		mockMetrics.EXPECT().SetKMMDevicePluginNum(numDevicePlugin)

		mhr.setKMMOMetrics(ctx)
	},
		Entry("build in container", true, false, false, false, false, nil, nil),
		Entry("build in KM", false, true, false, false, false, nil, nil),
		Entry("build in container and KM", true, true, false, false, false, nil, nil),
		Entry("sign in container", false, false, true, false, false, nil, nil),
		Entry("sign in KM", false, false, false, true, false, nil, nil),
		Entry("sign in container and KM", false, false, true, true, false, nil, nil),
		Entry("device plugin", false, false, false, false, true, nil, nil),
		Entry("modprobe args", false, false, false, false, false, []string{"param1", "param2"}, nil),
		Entry("modprobe raw args", false, false, false, false, false, nil, []string{"rawparam1", "rawparam2"}),
		Entry("altogether", true, true, true, true, true, []string{"param1", "param2"}, []string{"rawparam1", "rawparam2"}),
	)
})

var _ = Describe("ModuleReconciler_getExistingDS", func() {
	const (
		moduleName      = "moduleName"
		moduleNamespace = "moduleNamespace"
		kernelVersion   = "kernelVersion"
		moduleVersion   = "moduleVersion"
	)

	moduleLoaderLabels := map[string]string{
		constants.KernelLabel: kernelVersion,
		utils.GetModuleLoaderVersionLabelName(moduleNamespace, moduleName): moduleVersion,
	}

	devicePluginLabels := map[string]string{
		utils.GetDevicePluginVersionLabelName(moduleNamespace, moduleName): moduleVersion,
	}

	ds := appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: moduleNamespace, Name: moduleName},
	}

	It("empty list", func() {
		By("empty daemonset list")
		res := getExistingDS(nil, moduleNamespace, moduleName, kernelVersion, moduleVersion, false)
		Expect(res).To(BeNil())

		By("module loader, kernel version and module version are equal")
		ds.SetLabels(moduleLoaderLabels)
		res = getExistingDS([]appsv1.DaemonSet{ds}, moduleNamespace, moduleName, kernelVersion, moduleVersion, false)
		Expect(res).To(Equal(&ds))

		By("module loader, kernel version not equal, module version equal")
		res = getExistingDS([]appsv1.DaemonSet{ds}, moduleNamespace, moduleName, "some version", moduleVersion, false)
		Expect(res).To(BeNil())

		By("module loader, kernel version equal, module version not equal")
		res = getExistingDS([]appsv1.DaemonSet{ds}, moduleNamespace, moduleName, kernelVersion, "some version", false)
		Expect(res).To(BeNil())

		By("module loader, kernel version equal, module version label missing and module version parameter is empty")
		newLabels := map[string]string{
			constants.KernelLabel: kernelVersion,
		}
		ds.SetLabels(newLabels)
		res = getExistingDS([]appsv1.DaemonSet{ds}, moduleNamespace, moduleName, kernelVersion, "", false)
		Expect(res).To(Equal(&ds))

		By("device plugin, module version equal")
		ds.SetLabels(devicePluginLabels)
		res = getExistingDS([]appsv1.DaemonSet{ds}, moduleNamespace, moduleName, "", moduleVersion, true)
		Expect(res).To(Equal(&ds))

		By("device plugin, module version not equal")
		res = getExistingDS([]appsv1.DaemonSet{ds}, moduleNamespace, moduleName, "", "some version", true)
		Expect(res).To(BeNil())

		By("device plugin, module version label missing, and module version parameter is empty")
		ds.SetLabels(map[string]string{})
		res = getExistingDS([]appsv1.DaemonSet{ds}, moduleNamespace, moduleName, "", "", true)
		Expect(res).To(Equal(&ds))
	})
})