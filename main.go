package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/gorilla/mux"
	ipamv1 "github.com/metal3-io/ip-address-manager/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	APITimeout = time.Minute * 10
	Timeout    = time.Minute * 60
)

type IPAddressObject struct {
	IpPoolName string `json:"ipPoolName"`
	Namespace  string `json:"namespace"`
	IpAddress  string `json:"ipAddress"`
	Subnet     string `json:"subnet"`
}

type IPClaimObject struct {
	NetworkArmId string `json:"networkArmId"`
	HaksUuid     string `json:"haksUuid"`
	ReleaseIp    string `json:"releaseIp"`
	PodName      string `json:"podName"`
	PodNamespace string `json:"podNamespace"`
}

func main() {

	// creates the in-cluster config

	r := mux.NewRouter()
	r.HandleFunc("/", handler)
	r.HandleFunc("/getIPAddress", createIpClaim).Methods("POST")
	r.HandleFunc("/deleteIPAddress", deleteIpClaim).Methods("POST")
	r.HandleFunc("/health", healthHandler)
	r.HandleFunc("/readiness", readinessHandler)

	srv := &http.Server{
		Handler:      r,
		Addr:         ":8080",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Start Server
	go func() {
		log.Println("Starting Server")
		if err := srv.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()

	// Graceful Shutdown
	waitForShutdown(srv)

}

func handler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	name := query.Get("name")
	if name == "" {
		name = "Guest"
	}
	log.Printf("Received request for %s\n", name)
	w.Write([]byte(fmt.Sprintf("Hello, %s\n", name)))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func waitForShutdown(srv *http.Server) {
	interruptChan := make(chan os.Signal, 1)
	signal.Notify(interruptChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	// Block until we receive our signal.
	<-interruptChan

	// create a deadline to wait for.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	srv.Shutdown(ctx)

	log.Println("Shutting down")
	os.Exit(0)
}

func createIpClaim(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")

	var ipClaimObject IPClaimObject

	log.Printf("Received request for %v\n", r.Body)

	err := json.NewDecoder(r.Body).Decode(&ipClaimObject)
	if err != nil {
		log.Printf("error in parsing %v\n", err)

	}

	cl, ctx := getConnection()
	newIpAddressObject, _ := getIpaddress(cl, ctx, &ipClaimObject)

	json.NewEncoder(w).Encode(newIpAddressObject)

}

func deleteIpClaim(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")

	var ipClaimObject IPClaimObject

	_ = json.NewDecoder(r.Body).Decode(&ipClaimObject)
	cl, ctx := getConnection()
	ipAddressObject, _ := DeallocateIP(cl, ctx, &ipClaimObject)

	json.NewEncoder(w).Encode(ipAddressObject)

}

func getConnection() (client.Client, context.Context) {
	fmt.Println("Get Kubernetes rest config")
	fmt.Println()
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	fmt.Println("Get CRScheme")
	crScheme := runtime.NewScheme()
	appsv1.AddToScheme(crScheme)
	corev1.AddToScheme(crScheme)
	ipamv1.AddToScheme(crScheme)
	fmt.Println("Get Client")
	cl, err := client.New(config, client.Options{
		Scheme: crScheme,
	})
	if err != nil {
		fmt.Printf("Error in getting Client: %v\n", err)
		fmt.Println()
		os.Exit(1)
	}
	ctx := context.Background()

	return cl, ctx
}

func getIpaddress(cl client.Client, ctx context.Context, ipClaimObject *IPClaimObject) (*IPAddressObject, error) {

	fmt.Printf("ipClaimObject object  %v", ipClaimObject)
	fmt.Println()

	ipAddressObject := &IPAddressObject{
		IpPoolName: "l3network11-ipv4",
		Namespace:  "default",
	}
	foundIPPool := &ipamv1.IPPool{}

	err := cl.Get(ctx, apitypes.NamespacedName{Name: ipAddressObject.IpPoolName, Namespace: ipAddressObject.Namespace}, foundIPPool)
	if err != nil {
		log.Printf("could not get foundIPPool: %v", err)
		if apierrors.IsNotFound(err) {
			log.Printf("ip pool does not exist")
			return nil, err
		}
		log.Printf("could not get ippool: %v", err)
		return nil, err
	}

	log.Printf("getting IP pool ---------- ippool: %v ", *foundIPPool)

	log.Printf("getting IP pool Subnet---------- ippool.subnet: %v ", string(*foundIPPool.Spec.Pools[0].Subnet))
	_, ipnet, _ := net.ParseCIDR(string(*foundIPPool.Spec.Pools[0].Subnet))

	log.Printf("getting IP pool MASK---------- ippool.MASK: %v ", ipnet.Mask)

	if strings.EqualFold(ipClaimObject.ReleaseIp, "false") {
		reuseIpAddressObject, _ := getIpAddressObject(cl, ipClaimObject)
		if reuseIpAddressObject != nil {
			log.Printf("reuseIpAddressObject.Spec.Address: %s", reuseIpAddressObject.Spec.Address)
			log.Printf("reuseIpAddressObject.Spec.Prefix: %d", reuseIpAddressObject.Spec.Prefix)
			fullClaim := string(reuseIpAddressObject.Spec.Address) + "/" + fmt.Sprint(reuseIpAddressObject.Spec.Prefix)
			log.Printf("fullClaim: %s", fullClaim)
			ipAddressObject.IpAddress = string(reuseIpAddressObject.Spec.Address)
			ipAddressObject.Subnet = string(*foundIPPool.Spec.Pools[0].Subnet)
			return ipAddressObject, nil
		}
	}

	haksName := getObjectName(ipClaimObject.HaksUuid)
	networkarmusi := getObjectName(ipClaimObject.NetworkArmId)

	ipClaimName := haksName + "-" + networkarmusi + "-" + ipClaimObject.PodNamespace + "-" + ipClaimObject.PodName

	log.Printf("ipClaimName----------: %s ", ipClaimName)

	ipClaim := &ipamv1.IPClaim{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: ipClaimName,
			Namespace:    ipAddressObject.Namespace,
		},
		Spec: ipamv1.IPClaimSpec{
			Pool: corev1.ObjectReference{
				Name:      ipAddressObject.IpPoolName,
				Namespace: ipAddressObject.Namespace,
			},
		},
	}

	ipClaim.Labels = map[string]string{
		"hask-uiid":      getObjectName(ipClaimObject.HaksUuid),
		"network-arm-id": getObjectName(ipClaimObject.NetworkArmId),
		"pod-name":       ipClaimObject.PodName,
		"pod-namespace":  ipClaimObject.PodNamespace,
		"release-ip":     ipClaimObject.ReleaseIp,
	}

	log.Printf("Creating IP Claim -- ipClaim: %v ", ipClaim)

	err = createObject(cl, ctx, ipClaim)
	if err != nil {
		log.Printf("Error in creating ipclaim: %v", err)
		return nil, err
	}

	log.Printf("IP Claim created and waiting -- ipClaim: %v ", ipClaim)

	ipaddressError := WaitForNamespacedObject(cl, ipClaimObject, 1*time.Second, 10*time.Second)
	if ipaddressError != nil {
		return nil, ipaddressError
	}

	rnClaimIPAddress, err := getIpAddressObject(cl, ipClaimObject)
	if err != nil {
		return nil, err
	}
	log.Printf("rnClaimIPAddress.Spec.Address: %s", rnClaimIPAddress.Spec.Address)
	log.Printf("rnClaimIPAddress.Spec.Prefix: %d", rnClaimIPAddress.Spec.Prefix)
	fullClaim := string(rnClaimIPAddress.Spec.Address) + "/" + fmt.Sprint(rnClaimIPAddress.Spec.Prefix)
	log.Printf("fullClaim: %s", fullClaim)
	ipAddressObject.IpAddress = string(rnClaimIPAddress.Spec.Address)
	ipAddressObject.Subnet = string(*foundIPPool.Spec.Pools[0].Subnet)
	return ipAddressObject, nil

}

func createObject(cl client.Client, ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	err := cl.Create(ctx, obj.DeepCopyObject().(client.Object), opts...)
	if apierrors.IsAlreadyExists(err) {
		log.Printf("createIPv4Claim: ipclaim already exist: l3network-504-32eb7f2f-ipv4")
		return nil
	}
	return err
}

// DeallocateIP assigns an IP using a range and a reserve list.
func DeallocateIP(cl client.Client, ctx context.Context, ipClaimObject *IPClaimObject) (*IPAddressObject, error) {
	fmt.Printf("ipClaimObject object  %v", ipClaimObject)
	fmt.Println()

	ipAddressObject := &IPAddressObject{
		IpPoolName: "l3network11-ipv4",
		Namespace:  "default",
	}

	ipClaimList := &ipamv1.IPClaimList{}

	opts := []client.ListOption{
		client.MatchingLabels{
			"hask-uiid":      getObjectName(ipClaimObject.HaksUuid),
			"network-arm-id": getObjectName(ipClaimObject.NetworkArmId),
			"pod-name":       ipClaimObject.PodName,
			"pod-namespace":  ipClaimObject.PodNamespace,
			"release-ip":     ipClaimObject.ReleaseIp,
		},
	}
	err := cl.List(ctx, ipClaimList, opts...)
	if err != nil {
		return nil, err
	}

	ipClaim := ipClaimList.Items[0]

	log.Printf("Deleting the ipclaim:   %v", ipClaim)
	if strings.EqualFold(ipClaimObject.ReleaseIp, "false") {
		log.Printf("ipaddress will not be released for this: %v", ipClaim)
		return ipAddressObject, nil
	}

	err = cl.Delete(ctx, &ipClaim)
	if err != nil {
		return nil, fmt.Errorf("error in deleting the ipclaim")
	}

	log.Printf("Deallocating given previously used IP: %v", ipClaim.Status.Address)

	return ipAddressObject, nil
}

func WaitForNamespacedObject(c client.Client,
	ipClaimObject *IPClaimObject, retryInterval, timeout time.Duration) error {
	err := wait.PollImmediate(retryInterval, timeout, func() (done bool, err error) {
		ipAddressObject, err := getIpAddressObject(c, ipClaimObject)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if ipAddressObject != nil {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		fmt.Printf("failed to wait for obj  to exist: %v", err)
		return err
	}
	return nil
}

func getObjectName(uri string) string {
	return uri[strings.LastIndex(uri, "/")+1:]
}

func getIpAddressObject(c client.Client, ipClaimObject *IPClaimObject) (*ipamv1.IPAddress, error) {
	ctx, cancel := context.WithTimeout(context.Background(), APITimeout)
	defer cancel()
	opts := []client.ListOption{
		client.MatchingLabels{
			"hask-uiid":      getObjectName(ipClaimObject.HaksUuid),
			"network-arm-id": getObjectName(ipClaimObject.NetworkArmId),
			"pod-name":       ipClaimObject.PodName,
			"pod-namespace":  ipClaimObject.PodNamespace,
			"release-ip":     ipClaimObject.ReleaseIp,
		},
	}
	ipAddressList := &ipamv1.IPAddressList{}
	err := c.List(ctx, ipAddressList, opts...)
	if err != nil {
		return nil, err
	}
	if len(ipAddressList.Items) > 0 {
		ipaddress := ipAddressList.Items[0]
		return &ipaddress, nil
	}
	return nil, nil
}
