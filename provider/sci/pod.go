package sci

import (
	"context"
	"fmt"
	"github.com/virtual-kubelet/alibabacloud-eci/eci"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	"github.com/virtual-kubelet/virtual-kubelet/node/api/statsv1alpha1"
	"io"
	corev1 "k8s.io/api/core/v1"
)

// CreatePod accepts a Pod definition and creates
// an SCI deployment
func (p *SCIProvider) CreatePod(ctx context.Context, pod *corev1.Pod) error {
	////Ignore daemonSet Pod
	//if pod != nil && pod.OwnerReferences != nil && len(pod.OwnerReferences) != 0 && pod.OwnerReferences[0].Kind == "DaemonSet" {
	//	msg := fmt.Sprintf("Skip to create DaemonSet pod %q", pod.Name)
	//	log.G(ctx).WithField("Method", "CreatePod").Info(msg)
	//	return nil
	//}
	//
	//request := eci.CreateCreateContainerGroupRequest()
	//request.RestartPolicy = string(pod.Spec.RestartPolicy)
	//
	//// get containers
	//containers, err := p.getContainers(pod, false)
	//initContainers, err := p.getContainers(pod, true)
	//if err != nil {
	//	return err
	//}
	//
	//// get registry creds
	//creds, err := p.getImagePullSecrets(pod)
	//if err != nil {
	//	return err
	//}
	//
	//// get volumes
	//volumes, err := p.getVolumes(pod)
	//if err != nil {
	//	return err
	//}
	//
	//// assign all the things
	//request.Containers = containers
	//request.InitContainers = initContainers
	//request.Volumes = volumes
	//request.ImageRegistryCredentials = creds
	//CreationTimestamp := pod.CreationTimestamp.UTC().Format(podTagTimeFormat)
	//tags := []eci.Tag{
	//	eci.Tag{Key: "ClusterName", Value: p.clusterName},
	//	eci.Tag{Key: "NodeName", Value: p.nodeName},
	//	eci.Tag{Key: "NameSpace", Value: pod.Namespace},
	//	eci.Tag{Key: "PodName", Value: pod.Name},
	//	eci.Tag{Key: "UID", Value: string(pod.UID)},
	//	eci.Tag{Key: "CreationTimestamp", Value: CreationTimestamp},
	//}
	//
	//ContainerGroupName := containerGroupName(pod)
	//request.Tags = tags
	//request.SecurityGroupId = p.secureGroup
	//request.VSwitchId = p.vSwitch
	//request.ContainerGroupName = ContainerGroupName
	//msg := fmt.Sprintf("CreateContainerGroup request %+v", request)
	//log.G(ctx).WithField("Method", "CreatePod").Info(msg)
	//response, err := p.eciClient.CreateContainerGroup(request)
	//if err != nil {
	//	return err
	//}
	//msg = fmt.Sprintf("CreateContainerGroup successed. %s, %s, %s", response.RequestId, response.ContainerGroupId, ContainerGroupName)
	//log.G(ctx).WithField("Method", "CreatePod").Info(msg)
	//return nil
}

func (p *SCIProvider) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
}

func (p *SCIProvider) GetPodStatus(ctx context.Context, namespace, name string) (*corev1.PodStatus, error) {
}

func (p *SCIProvider) GetPods(context.Context) ([]*corev1.Pod, error) {}

func (p *SCIProvider) UpdatePod(ctx context.Context, pod *corev1.Pod) error {
}

func (p *SCIProvider) DeletePod(ctx context.Context, pod *corev1.Pod) error {

}

func (p *SCIProvider) GetContainerLogs(ctx context.Context, namespace, podName, containerName string, opts api.ContainerLogOpts) (io.ReadCloser, error) {

}

func (p *SCIProvider) RunInContainer(ctx context.Context, namespace, podName, containerName string, cmd []string, attach api.AttachIO) error {

}

func (p *SCIProvider) GetStatsSummary(context.Context) (*statsv1alpha1.Summary, error) {

}
