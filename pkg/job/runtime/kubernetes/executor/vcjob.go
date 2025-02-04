/*
Copyright (c) 2021 PaddlePaddle Authors. All Rights Reserve.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package executor

import (
	"fmt"
	"strconv"

	log "github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	vcjob "volcano.sh/apis/pkg/apis/batch/v1alpha1"

	"github.com/PaddlePaddle/PaddleFlow/pkg/apiserver/models"
	"github.com/PaddlePaddle/PaddleFlow/pkg/common/config"
	"github.com/PaddlePaddle/PaddleFlow/pkg/common/errors"
	"github.com/PaddlePaddle/PaddleFlow/pkg/common/k8s"
	"github.com/PaddlePaddle/PaddleFlow/pkg/common/schema"
)

const (
	psPort int32 = 8001
)

type VCJob struct {
	KubeJob
	JobModeParams
}

func (vj *VCJob) validateJob() error {
	if err := vj.KubeJob.validateJob(); err != nil {
		return err
	}
	if len(vj.JobMode) == 0 {
		// patch default value
		vj.JobMode = schema.EnvJobModePod
	}

	var err error
	switch vj.JobMode {
	case schema.EnvJobModePod:
		err = vj.validatePodMode()
	case schema.EnvJobModePS:
		err = vj.validatePSMode()
	case schema.EnvJobModeCollective:
		err = vj.validateCollectiveMode()
	default:
		return errors.InvalidJobModeError(vj.JobMode)
	}
	return err
}

// patchVCJobVariable patch env variable to vcJob, the order of patches following vcJob crd
func (vj *VCJob) patchVCJobVariable(jobApp *vcjob.Job, jobID string) error {
	jobApp.Name = jobID
	// metadata
	jobApp.Namespace = vj.Namespace
	if jobApp.Labels == nil {
		jobApp.Labels = map[string]string{}
	}
	jobApp.Labels[schema.JobOwnerLabel] = schema.JobOwnerValue
	jobApp.Labels[schema.JobIDLabel] = jobID

	if len(vj.QueueName) > 0 {
		jobApp.Spec.Queue = vj.QueueName
		priorityClass := vj.getPriorityClass()
		jobApp.Spec.PriorityClassName = priorityClass
	}
	// SchedulerName
	jobApp.Spec.SchedulerName = config.GlobalServerConfig.Job.SchedulerName

	var err error
	switch vj.JobMode {
	case schema.EnvJobModePS:
		err = vj.fillPSJobSpec(jobApp)
	case schema.EnvJobModeCollective:
		err = vj.fillCollectiveJobSpec(jobApp)
	case schema.EnvJobModePod:
		err = vj.fillPodJobSpec(jobApp)
	}
	if err != nil {
		log.Errorf("patchVCJobVariable failed, err=[%v]", err)
		return err
	}
	return nil

}

func (vj *VCJob) CreateJob() (string, error) {
	if err := vj.validateJob(); err != nil {
		log.Errorf("validate job ailed, err %v", err)
		return "", err
	}
	jobID := vj.GetID()
	log.Debugf("begin create job jobID:[%s]", jobID)

	jobApp := &vcjob.Job{}
	if err := vj.createJobFromYaml(jobApp); err != nil {
		log.Errorf("create job failed, err %v", err)
		return "", err
	}

	vj.patchVCJobVariable(jobApp, jobID)

	log.Debugf("begin submit job jobID:[%s], jobApp:[%v]", jobID, jobApp)
	err := Create(jobApp, k8s.VCJobGVK, vj.DynamicClientOption)
	if err != nil {
		log.Errorf("create job %v failed, err %v", jobID, err)
		return "", err
	}
	return jobID, nil
}

func (vj *VCJob) StopJobByID(jobID string) error {
	job, err := models.GetJobByID(jobID)
	if err != nil {
		return err
	}
	namespace := job.Config.GetNamespace()
	if err = Delete(namespace, job.ID, k8s.VCJobGVK, vj.DynamicClientOption); err != nil {
		log.Errorf("stop vcjob %s in namespace %s failed, err %v", job.ID, namespace, err)
		return err
	}
	return nil
}

func (vj *VCJob) fillPSJobSpec(jobSpec *vcjob.Job) error {
	vj.Env[schema.EnvJobPSPort] = strconv.FormatInt(int64(psPort), 10)

	// ps mode only permit 2 tasks
	if len(jobSpec.Spec.Tasks) != 2 {
		return fmt.Errorf("vcjob[%s] must be contain two Tasks, actually [%d]", jobSpec.Name, len(jobSpec.Spec.Tasks))
	}
	for _, task := range vj.Tasks {
		log.Warningf("vcjob cannot recognize which task is ps or worker, task[%v]", task)
		if task.Role == schema.RolePServer {
			// ps master
			if err := vj.fillTaskInPSMode(&jobSpec.Spec.Tasks[0], task, jobSpec.Name); err != nil {
				log.Errorf("fill Task[%s] in PS-Mode failed, err=[%v]", jobSpec.Spec.Tasks[0].Name, err)
				return err
			}
		} else {
			// worker
			if err := vj.fillTaskInPSMode(&jobSpec.Spec.Tasks[1], task, jobSpec.Name); err != nil {
				log.Errorf("fill Task[%s] in PS-Mode failed, err=[%v]", jobSpec.Spec.Tasks[1].Name, err)
				return err
			}
		}
	}

	jobSpec.Spec.MinAvailable = jobSpec.Spec.Tasks[0].Replicas + jobSpec.Spec.Tasks[1].Replicas

	return nil
}

func (vj *VCJob) fillTaskInPSMode(vcTask *vcjob.TaskSpec, task models.Member, jobName string) error {
	log.Infof("fill Task[%s] in PS-Mode", vcTask.Name)
	vcTask.Replicas = int32(task.Replicas)

	if vcTask.Replicas <= 0 {
		vcTask.Replicas = defaultPSReplicas
	}

	// patch vcTask.Template.Labels
	if vcTask.Template.Labels == nil {
		vcTask.Template.Labels = map[string]string{}
	}
	vcTask.Template.Labels[schema.JobIDLabel] = jobName

	// patch Task.Template.Spec.Containers[0]
	if len(vcTask.Template.Spec.Containers) != 1 {
		vcTask.Template.Spec.Containers = []v1.Container{{}}
	}
	vj.fillContainerInTasks(&vcTask.Template.Spec.Containers[0], task)
	vcTask.Template.Spec.Containers[0].VolumeMounts = vj.appendMountIfAbsent(vcTask.Template.Spec.Containers[0].VolumeMounts,
		vj.generateVolumeMount())

	// patch vcTask.Template.Spec.Volumes
	vcTask.Template.Spec.Volumes = vj.appendVolumeIfAbsent(vcTask.Template.Spec.Volumes, vj.generateVolume())

	return nil
}

func (vj *VCJob) fillPodJobSpec(jobSpec *vcjob.Job) error {
	log.Debugf("fillPodJobSpec for job[%s]", jobSpec.Name)
	if jobSpec.Spec.Tasks == nil {
		return fmt.Errorf("tasks is nil")
	}

	for i := range jobSpec.Spec.Tasks {
		if err := vj.fillTaskInPodMode(&jobSpec.Spec.Tasks[i], jobSpec.Name); err != nil {
			log.Errorf("fillTaskInPodMode occur a err[%v]", err)
			return err
		}
	}
	log.Debugf("job[%s].Spec.Tasks=[%+v]", jobSpec.Name, jobSpec.Spec.Tasks)
	return nil
}

// fillTaskInPodMode fill params into job's task in vcJob pod mode
func (vj *VCJob) fillTaskInPodMode(taskSpec *vcjob.TaskSpec, jobName string) error {
	log.Infof("fillTaskInPodMode: fill params in job[%s]-task[%s]", jobName, taskSpec.Name)

	if taskSpec.Replicas <= 0 {
		taskSpec.Replicas = defaultPodReplicas
	}

	// filter illegal task
	// only default yaml job can be patched,
	// user yaml may be muti-containers, and we cannot ensure format of user's yaml
	if taskSpec.Template.Spec.Containers == nil || len(taskSpec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("task's container is nil")
	}

	// patch taskSpec.Template.Labels
	if taskSpec.Template.Labels == nil {
		taskSpec.Template.Labels = map[string]string{}
	}
	taskSpec.Template.Labels[schema.JobIDLabel] = jobName

	if len(vj.Tasks) != 1 || len(vj.Tasks[0].Flavour.CPU) == 0 || len(vj.Tasks[0].Flavour.Mem) == 0 {
		log.Errorf("vcjob[%s]'s flavour is absent, j.Tasks=[%+v]", jobName, vj.Tasks)
		return fmt.Errorf("vcjob[%s]'s flavour is absent", jobName)
	}
	// patch taskSpec.Template.Spec.Containers
	vj.fillContainerInVcJob(&taskSpec.Template.Spec.Containers[0], vj.Tasks[0].Flavour, vj.Command)

	// patch taskSpec.Template.Spec.Volumes
	taskSpec.Template.Spec.Volumes = vj.appendVolumeIfAbsent(taskSpec.Template.Spec.Volumes, vj.generateVolume())
	log.Debugf("fillTaskInPodMode completed: job[%s]-task[%+v]", jobName, taskSpec)
	return nil
}

func (vj *VCJob) fillCollectiveJobSpec(jobSpec *vcjob.Job) error {
	if len(vj.CollectiveJobReplicas) > 0 {
		replicas, _ := strconv.Atoi(vj.CollectiveJobReplicas)
		jobSpec.Spec.MinAvailable = int32(replicas)
	}

	var err error
	if jobSpec.Spec.Tasks, err = vj.fillTaskInCollectiveMode(jobSpec.Spec.Tasks, jobSpec.Name); err != nil {
		log.Errorf("fillTaskInCollectiveMode for job[%s] failed, err=[%v]", jobSpec.Name, err)
		return err
	}

	return nil
}

func (vj *VCJob) fillTaskInCollectiveMode(tasks []vcjob.TaskSpec, jobName string) ([]vcjob.TaskSpec, error) {
	log.Debugf("fillTaskInCollectiveMode: job[%s]-task", jobName)

	// filter illegal job
	if len(tasks) != 1 {
		return nil, fmt.Errorf("the num of job[%s]-task must be 1, current is [%d]", jobName, len(tasks))
	}
	if len(tasks[0].Template.Spec.Containers) != 1 {
		return nil, fmt.Errorf("the num of job[%s]-task[%s]-container must be 1, current is [%d]", jobName, tasks[0].Name,
			len(tasks[0].Template.Spec.Containers))
	}

	// task.Metadata and labels
	task := &tasks[0]
	if len(vj.CollectiveJobReplicas) > 0 {
		replicas, _ := strconv.Atoi(vj.CollectiveJobReplicas)
		task.Replicas = int32(replicas)
	}
	if task.Replicas <= 0 {
		task.Replicas = defaultCollectiveReplicas
	}

	if task.Template.Labels == nil {
		task.Template.Labels = map[string]string{}
	}
	task.Template.Labels[schema.JobIDLabel] = jobName

	if len(vj.Tasks) != 1 {
		return nil, fmt.Errorf("the num of job[%s]-task must be 1, current is [%d]", jobName, len(vj.Tasks))
	}
	// todo : add affinity
	vj.fillContainerInTasks(&task.Template.Spec.Containers[0], vj.Tasks[0])
	task.Template.Spec.Containers[0].VolumeMounts = vj.appendMountIfAbsent(task.Template.Spec.Containers[0].VolumeMounts,
		vj.generateVolumeMount())
	// patch task.Template.Spec.Volumes
	task.Template.Spec.Volumes = vj.appendVolumeIfAbsent(task.Template.Spec.Volumes, vj.generateVolume())

	return tasks, nil
}
