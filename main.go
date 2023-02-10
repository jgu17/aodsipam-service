package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	APITimeout       = time.Minute * 10
	Timeout          = time.Minute * 60
	webhookdaemonset = "ds-mutate-webhook-with-hook"
	annotationName   = "platform.afo-nc.microsoft.com/dswhookjobcnt"
)

func main() {
	// creates the in-cluster config
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

	fmt.Println("Getting daemon set")
	var dsnamespace string
	for {
		isDSRunning, namespace, err := isDaemonSetRunning(cl, ctx)
		if err != nil {
			fmt.Printf("Error in function isDaemonSetRunning: %v\n", err)
			fmt.Println()
			os.Exit(1)
		}
		if !isDSRunning {
			fmt.Println("waiting for 30 seconds as DS is not running")
			time.Sleep(30 * time.Second)
		} else {
			fmt.Println("DS is running ")
			dsnamespace = namespace
			break
		}
	}

	for {
		isPODRunning, err := isPodRunning(cl, ctx, dsnamespace)
		if err != nil {
			fmt.Printf("Error in function isPodRunning: %v\n", err)
			fmt.Println()
			os.Exit(1)
		}
		if !isPODRunning {
			fmt.Println("waiting for 30 seconds as POD is not running")
			time.Sleep(30 * time.Second)
		} else {
			fmt.Println("POD is running ")
			break
		}
	}

	daemonSetList := &appsv1.DaemonSetList{}
	err = cl.List(ctx, daemonSetList)
	if err != nil {
		fmt.Printf("Error in getting Daemon set: %v\n", err)
		fmt.Println()
		os.Exit(1)
	}

	for _, daemonSet := range daemonSetList.Items {
		if daemonSet.Annotations == nil {
			daemonSet.Annotations = map[string]string{}
		}
		fmt.Printf("seetting daemon set %s", daemonSet.Name)
		fmt.Println()
		if daemonSet.GetAnnotations()[annotationName] == "" {
			daemonSet.GetAnnotations()[annotationName] = "0"
		} else {
			intVar, _ := strconv.Atoi(daemonSet.GetAnnotations()[annotationName])

			daemonSet.GetAnnotations()[annotationName] = strconv.Itoa(intVar + 1)
		}

		annotationUpdateError := cl.Update(ctx, &daemonSet)
		if annotationUpdateError != nil {
			fmt.Printf("Error in updating Daemon se: %v\n", annotationUpdateError)
			fmt.Println()
			os.Exit(1)
		}
		fmt.Printf("updated daemon set %s", daemonSet.Name)
		fmt.Println()
	}
}

func isDaemonSetRunning(cl client.Client, ctx context.Context) (bool, string, error) {
	daemonSetList := &appsv1.DaemonSetList{}
	err := cl.List(ctx, daemonSetList)
	if err != nil {
		fmt.Printf("Error in getting Daemon set: %v\n", err)
		fmt.Println()
		return false, "", err
	}
	for _, daemonSet := range daemonSetList.Items {
		fmt.Printf("daemonset name retrived: %v\n", daemonSet.Name)
		fmt.Println()
		if strings.EqualFold(daemonSet.Name, webhookdaemonset) {
			fmt.Printf("daemonset name found: %v\n", daemonSet.Name)
			return true, daemonSet.Namespace, nil
		}
	}

	return false, "", nil
}

func isPodRunning(cl client.Client, ctx context.Context, namespace string) (bool, error) {
	podList := &corev1.PodList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{
			"app": webhookdaemonset,
		},
	}
	err := cl.List(ctx, podList, opts...)
	if err != nil {
		fmt.Printf("Error in getting Daemon set pod: %v\n", err)
		fmt.Println()
		return false, err
	}

	fmt.Println("Iterating pods")
	for _, pod := range podList.Items {
		fmt.Printf("pods name retrived: %v\n", pod.Name)
		fmt.Println()
		if !(pod.Status.Phase == corev1.PodRunning) {
			fmt.Printf("pods %s status is %v:\n", pod.Name, pod.Status.Phase)
			fmt.Println()
			return false, nil
		}
		for _, container := range pod.Status.ContainerStatuses {
			fmt.Printf("container name retrived: %v\n", container.Name)
			if !(container.Ready) {
				fmt.Printf("container %s status is %v:\n", container.Name, container.Ready)
				fmt.Println()
				return false, nil
			}

		}

	}
	return true, nil
}
