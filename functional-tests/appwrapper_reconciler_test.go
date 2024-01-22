package functional_tests

import (
	"context"
	"go/build"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	gstruct "github.com/onsi/gomega/gstruct"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	. "github.com/project-codeflare/codeflare-common/support"
	"github.com/project-codeflare/instascale/controllers"
	"github.com/project-codeflare/instascale/pkg/config"
	mcadv1beta1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	log "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	ctx    context.Context
	cancel context.CancelFunc
)

func logger() {
	// Get log file path from environment variable or use a default path
	logFilePath := os.Getenv("LOG_FILE_PATH")
	if logFilePath == "" {
		logFilePath = "./functional-test-logfile.log"
	}
	// Create a log file
	logFile, err := os.Create(logFilePath)
	if err != nil {
		log.Log.Error(err, "Error creating log file: %v", err)
	}
	// Configure zap logger to write to the log file
	logger := zap.New(zap.WriteTo(logFile), zap.UseDevMode(true))

	// Set the logger for controller-runtime
	ctrl.SetLogger(logger)

	// This line prevents controller-runtime from complaining about log.SetLogger never being called
	log.SetLogger(logger)
}

func startEnvTest(t *testing.T) *rest.Config {
	// to redirect all functional test related logs to separate logfile ~ default (in functional-tests directory), can be changed using environment variable LOG_FILE_PATH=/path_to_logfile
	logger()

	//specify testEnv configuration
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(build.Default.GOPATH, "pkg", "mod", "github.com", "project-codeflare", "multi-cluster-app-dispatcher@v1.39.0", "config", "crd", "bases"),
			filepath.Join(build.Default.GOPATH, "pkg", "mod", "github.com", "openshift", "api@v0.0.0-20230213134911-7ba313770556", "machine", "v1beta1"),
		},
		ErrorIfCRDPathMissing: true,
	}
	test := WithConfig(t, testEnv.Config)
	cfg, err := testEnv.Start()
	test.Expect(err).NotTo(HaveOccurred())
	test.Expect(cfg).NotTo(BeNil())

	t.Cleanup(func() {
		teardownTestEnv(test, testEnv)
	})

	return cfg
}

func startInstascaleController(test Test, cfg *rest.Config) {
	ctx, cancel = context.WithCancel(context.TODO())

	// Add custom resource schemes to the global scheme.
	err := mcadv1beta1.AddToScheme(scheme.Scheme)
	test.Expect(err).NotTo(HaveOccurred())

	err = machinev1beta1.AddToScheme(scheme.Scheme)
	test.Expect(err).NotTo(HaveOccurred())

	// Create a controller manager for managing controllers.
	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	test.Expect(err).ToNot(HaveOccurred())

	// Create an instance of the AppWrapperReconciler with configuration
	instaScaleController := &controllers.AppWrapperReconciler{
		Client: k8sManager.GetClient(),
		Scheme: k8sManager.GetScheme(),
		Config: config.InstaScaleConfiguration{
			MachineSetsStrategy: "reuse",
			MaxScaleoutAllowed:  5,
		},
	}
	// Set up the AppWrapperReconciler with the manager.
	err = instaScaleController.SetupWithManager(context.Background(), k8sManager)
	test.Expect(err).ToNot(HaveOccurred())

	// Start the controller manager in a goroutine.
	go func() {
		err = k8sManager.Start(ctx)
		test.Expect(err).ToNot(HaveOccurred())
	}()

}

func teardownTestEnv(test Test, testEnv *envtest.Environment) {
	cancel()
	err := testEnv.Stop()
	test.Expect(err).NotTo(HaveOccurred())
}

func instascaleAppwrapper(namespace string) *mcadv1beta1.AppWrapper {
	aw := &mcadv1beta1.AppWrapper{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instascale",
			Namespace: namespace,
			Labels: map[string]string{
				"orderedinstance": "test.instance1",
			},
			Finalizers: []string{"instascale.codeflare.dev/finalizer"},
		},
		Spec: mcadv1beta1.AppWrapperSpec{
			AggrResources: mcadv1beta1.AppWrapperResourceList{
				GenericItems: []mcadv1beta1.AppWrapperGenericResource{
					{
						DesiredAvailable: 1,
						CustomPodResources: []mcadv1beta1.CustomPodResourceTemplate{
							{
								Replicas: 1,
								Requests: apiv1.ResourceList{
									apiv1.ResourceCPU:    resource.MustParse("250m"),
									apiv1.ResourceMemory: resource.MustParse("512Mi"),
								},
								Limits: apiv1.ResourceList{
									apiv1.ResourceCPU:    resource.MustParse("1"),
									apiv1.ResourceMemory: resource.MustParse("1G"),
								},
							},
						},
					},
				},
			},
		},
	}
	return aw
}

func TestReconciler(t *testing.T) {
	// initiate setting up the EnvTest environment
	cfg := startEnvTest(t)

	test := WithConfig(t, cfg)
	startInstascaleController(test, cfg) // setup client required for test to interact with kubernetes machine API

	// initialize a variable with test client
	client := test.Client()

	//read machineset yaml from file `test_instascale_machineset.yml`
	b, err := os.ReadFile("test_instascale_machineset.yml")
	test.Expect(err).ToNot(HaveOccurred())

	//deserialize kubernetes object
	decode := scheme.Codecs.UniversalDeserializer().Decode
	ms, _, err := decode(b, nil, nil) //decode machineset content of YAML file into kubernetes object
	test.Expect(err).ToNot(HaveOccurred())
	msa := ms.(*machinev1beta1.MachineSet) //asserts that decoded object is of type `*machinev1beta1.MachineSet`

	//create machineset in default namespace
	ms, err = client.Machine().MachineV1beta1().MachineSets("default").Create(test.Ctx(), msa, metav1.CreateOptions{})
	test.Expect(err).ToNot(HaveOccurred())

	//assert that the replicas in Machineset specification is 0, before appwrapper creation
	test.Eventually(MachineSet(test, "default", "test-instascale"), 10*time.Second).Should(WithTransform(MachineSetReplicas, gstruct.PointTo(Equal(int32(0)))))

	//create new test namespace
	namespace := test.NewTestNamespace()

	// initializes an appwrapper in the created namespace
	aw := instascaleAppwrapper(namespace.Name)

	// create appwrapper resource using mcadClient
	aw, err = client.MCAD().WorkloadV1beta1().AppWrappers(namespace.Name).Create(test.Ctx(), aw, metav1.CreateOptions{})
	test.Expect(err).ToNot(HaveOccurred())

	//update appwrapper status to avoid appwrapper dispatch in case of Insufficient resources
	aw.Status = mcadv1beta1.AppWrapperStatus{
		Pending: 1,
		State:   mcadv1beta1.AppWrapperStateEnqueued,
		Conditions: []mcadv1beta1.AppWrapperCondition{
			{
				Type:    mcadv1beta1.AppWrapperCondBackoff,
				Status:  apiv1.ConditionTrue,
				Reason:  "AppWrapperNotRunnable",
				Message: "Insufficient resources to dispatch AppWrapper.",
			},
		},
	}
	_, err = client.MCAD().WorkloadV1beta1().AppWrappers(namespace.Name).UpdateStatus(test.Ctx(), aw, metav1.UpdateOptions{})
	test.Expect(err).ToNot(HaveOccurred())

	// assert for machine replicas belonging to the machine set after appwrapper creation- there should be 1
	test.Eventually(MachineSet(test, "default", "test-instascale"), 10*time.Second).Should(WithTransform(MachineSetReplicas, gstruct.PointTo(Equal(int32(1)))))

	// delete appwrapper
	err = client.MCAD().WorkloadV1beta1().AppWrappers(namespace.Name).Delete(test.Ctx(), aw.Name, metav1.DeleteOptions{})
	test.Expect(err).ToNot(HaveOccurred())

	// assert for machine belonging to the machine set after appwrapper deletion- there should be none
	test.Eventually(MachineSet(test, "default", "test-instascale"), 10*time.Second).Should(WithTransform(MachineSetReplicas, gstruct.PointTo(Equal(int32(0)))))

	// delete machineset
	err = client.Machine().MachineV1beta1().MachineSets("default").Delete(test.Ctx(), "test-instascale", metav1.DeleteOptions{})
	test.Expect(err).ToNot(HaveOccurred())

}
