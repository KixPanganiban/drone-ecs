package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
)

type TaskContainer struct {
	Name              string
	DockerImage       string
	Tag               string
	PortMappings      string
	CPU               int64
	Memory            int64
	MemoryReservation int64
}

type Plugin struct {
	Key                     string
	Secret                  string
	Region                  string
	Family                  string
	TaskRoleArn             string
	Containers              []string
	Service                 string
	Cluster                 string
	LogDriver               string
	LogOptions              []string
	DeploymentConfiguration string
	Environment             []string
	SecretEnvironment       []string
	Labels                  []string
	EntryPoint              []string
	DesiredCount            int64
	NetworkMode             string
	YamlVerified            bool
	TaskCPU                 string
	TaskMemory              string
	TaskExecutionRoleArn    string
	Compatibilities         string
	HealthCheckCommand      []string
	HealthCheckInterval     int64
	HealthCheckRetries      int64
	HealthCheckStartPeriod  int64
	HealthCheckTimeout      int64
	Ulimits                 []string

	// ServiceNetworkAssignPublicIP - Whether the task's elastic network interface receives a public IP address. The default value is DISABLED.
	ServiceNetworkAssignPublicIp string

	// ServiceNetworkSecurityGroups represents the VPC security groups to use
	// when running awsvpc network mode.
	ServiceNetworkSecurityGroups []string

	// ServiceNetworkSubnets represents the VPC security groups to use when
	// running awsvpc network mode.
	ServiceNetworkSubnets []string
}

const (
	containerStringParseErr           = "error parsing container string: "
	softLimitBaseParseErr             = "error parsing ulimits softLimit: "
	hardLimitBaseParseErr             = "error parsing ulimits hardLimit: "
	hostPortBaseParseErr              = "error parsing port_mappings hostPort: "
	containerBaseParseErr             = "error parsing port_mappings containerPort: "
	minimumHealthyPercentBaseParseErr = "error parsing deployment_configuration minimumHealthyPercent: "
	maximumPercentBaseParseErr        = "error parsing deployment_configuration maximumPercent: "
)

// DescribeServices returns an ecs.DescribeServicesOutput based on the service given by cluster and service
func DescribeServices(svc *ecs.ECS, cluster string, service string) (*ecs.DescribeServicesOutput, error) {
	describeServicesInput := ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: []*string{aws.String(service)},
	}
	return svc.DescribeServices(&describeServicesInput)
}

func (p *Plugin) Exec() error {
	fmt.Println("Drone AWS ECS Plugin built")
	awsConfig := aws.Config{}

	if len(p.Key) != 0 && len(p.Secret) != 0 {
		awsConfig.Credentials = credentials.NewStaticCredentials(p.Key, p.Secret, "")
	}
	awsConfig.Region = aws.String(p.Region)
	svc := ecs.New(session.New(&awsConfig))

	describeServicesOutput, describeServicesErr := DescribeServices(svc, p.Cluster, p.Service)
	if describeServicesErr != nil {
		fmt.Println(describeServicesErr.Error())
		return describeServicesErr
	}

	describeTaskDefinitionInput := ecs.DescribeTaskDefinitionInput{
		TaskDefinition: describeServicesOutput.Services[0].TaskDefinition,
	}
	describeTaskDefinitionOutput, describeTaskDefinitionErr := svc.DescribeTaskDefinition(&describeTaskDefinitionInput)
	if describeTaskDefinitionErr != nil {
		fmt.Println(describeTaskDefinitionErr.Error())
		return describeTaskDefinitionErr
	}
	currentTaskDefinition := describeTaskDefinitionOutput.TaskDefinition

	definitions := []*ecs.ContainerDefinition{}
	containers := []*TaskContainer{}

	for _, containerString := range p.Containers {
		containerAttrs := strings.Split(containerString, ":")
		var newContainer TaskContainer
		if len(containerAttrs) < 3 {
			return errors.New(hostPortBaseParseErr + containerString)
		}
		if len(containerAttrs) >= 3 {
			newContainer = TaskContainer{
				Name:        containerAttrs[0],
				DockerImage: containerAttrs[1],
				Tag:         containerAttrs[2],
			}
		}
		if len(containerAttrs) >= 4 {
			newContainer.PortMappings = containerAttrs[3]

		}
		if len(containerAttrs) >= 5 {
			newContainer.CPU, _ = strconv.ParseInt(containerAttrs[4], 10, 64)

		}
		if len(containerAttrs) >= 6 {
			newContainer.MemoryReservation, _ = strconv.ParseInt(containerAttrs[5], 10, 64)
		}
		if len(containerAttrs) >= 7 {
			newContainer.Memory, _ = strconv.ParseInt(containerAttrs[6], 10, 64)
		}
		containers = append(containers, &newContainer)
	}

	for _, taskContainer := range containers {
		Image := taskContainer.DockerImage + ":" + taskContainer.Tag
		if len(taskContainer.Name) == 0 {
			taskContainer.Name = p.Family + "-container"
		}
		definition := p.getOrCreateContainerDefinition(
			currentTaskDefinition,
			taskContainer.Name,
			Image,
		)

		if taskContainer.CPU != 0 {
			definition.Cpu = aws.Int64(taskContainer.CPU)
		}

		if taskContainer.Memory == 0 && taskContainer.MemoryReservation == 0 {
			definition.MemoryReservation = aws.Int64(128)
		} else {
			// if taskContainer.Memory != 0 {
			// 	definition.Memory = aws.Int64(taskContainer.Memory)
			// }
			if taskContainer.MemoryReservation != 0 {
				definition.MemoryReservation = aws.Int64(taskContainer.MemoryReservation)
			}
		}

		// Port mappings
		cleanedPortMapping := strings.Trim(taskContainer.PortMappings, " ")
		if len(cleanedPortMapping) > 0 && len(definition.PortMappings) == 0 {
			parts := strings.SplitN(cleanedPortMapping, " ", 2)
			hostPort, hostPortErr := strconv.ParseInt(parts[0], 10, 64)
			if hostPortErr != nil {
				hostPortWrappedErr := errors.New(hostPortBaseParseErr + hostPortErr.Error())
				fmt.Println(hostPortWrappedErr.Error())
				return hostPortWrappedErr
			}
			containerPort, containerPortErr := strconv.ParseInt(parts[1], 10, 64)
			if containerPortErr != nil {
				containerPortWrappedErr := errors.New(containerBaseParseErr + containerPortErr.Error())
				fmt.Println(containerPortWrappedErr.Error())
				return containerPortWrappedErr
			}

			pair := ecs.PortMapping{
				ContainerPort: aws.Int64(containerPort),
				HostPort:      aws.Int64(hostPort),
				Protocol:      aws.String("TransportProtocol"),
			}

			definition.PortMappings = []*ecs.PortMapping{&pair}
		}

		if len(definition.Environment) == 0 {
			// Environment variables
			for _, envVar := range p.Environment {
				parts := strings.SplitN(envVar, "=", 2)
				pair := ecs.KeyValuePair{
					Name:  aws.String(strings.Trim(parts[0], " ")),
					Value: aws.String(strings.Trim(parts[1], " ")),
				}
				definition.Environment = append(definition.Environment, &pair)
			}

			// Secret Environment variables
			for _, envVar := range p.SecretEnvironment {
				parts := strings.SplitN(envVar, "=", 2)
				pair := ecs.KeyValuePair{}
				if len(parts) == 2 {
					// set to custom named variable
					pair.SetName(aws.StringValue(aws.String(strings.Trim(parts[0], " "))))
					pair.SetValue(aws.StringValue(aws.String(os.Getenv(strings.Trim(parts[1], " ")))))
				} else if len(parts) == 1 {
					// default to named var
					pair.SetName(aws.StringValue(aws.String(parts[0])))
					pair.SetValue(aws.StringValue(aws.String(os.Getenv(parts[0]))))
				} else {
					fmt.Println("invalid syntax in secret enironment var", envVar)
				}
				definition.Environment = append(definition.Environment, &pair)
			}
		}

		if len(definition.Ulimits) == 0 {
			// Ulimits
			for _, uLimit := range p.Ulimits {
				cleanedULimit := strings.Trim(uLimit, " ")
				parts := strings.SplitN(cleanedULimit, " ", 3)
				name := strings.Trim(parts[0], " ")
				softLimit, softLimitErr := strconv.ParseInt(parts[1], 10, 64)
				if softLimitErr != nil {
					softLimitWrappedErr := errors.New(softLimitBaseParseErr + softLimitErr.Error())
					fmt.Println(softLimitWrappedErr.Error())
					return softLimitWrappedErr
				}
				hardLimit, hardLimitErr := strconv.ParseInt(parts[2], 10, 64)
				if hardLimitErr != nil {
					hardLimitWrappedErr := errors.New(hardLimitBaseParseErr + hardLimitErr.Error())
					fmt.Println(hardLimitWrappedErr.Error())
					return hardLimitWrappedErr
				}

				pair := ecs.Ulimit{
					Name:      aws.String(name),
					HardLimit: aws.Int64(hardLimit),
					SoftLimit: aws.Int64(softLimit),
				}

				definition.Ulimits = append(definition.Ulimits, &pair)
			}
		}

		if len(definition.DockerLabels) == 0 {
			// DockerLabels
			for _, label := range p.Labels {
				parts := strings.SplitN(label, "=", 2)
				definition.DockerLabels[strings.Trim(parts[0], " ")] = aws.String(strings.Trim(parts[1], " "))
			}
		}

		if len(definition.EntryPoint) == 0 {
			// EntryPoint
			for _, v := range p.EntryPoint {
				definition.EntryPoint = append(definition.EntryPoint, &v)
			}
		}

		// LogOptions
		if len(p.LogDriver) > 0 {
			definition.LogConfiguration = new(ecs.LogConfiguration)
			definition.LogConfiguration.LogDriver = &p.LogDriver
			if len(p.LogOptions) > 0 {
				definition.LogConfiguration.Options = make(map[string]*string)
				for _, logOption := range p.LogOptions {
					parts := strings.SplitN(logOption, "=", 2)
					logOptionKey := strings.Trim(parts[0], " ")
					logOptionValue := aws.String(strings.Trim(parts[1], " "))
					definition.LogConfiguration.Options[logOptionKey] = logOptionValue
				}
			}
		}

		if len(p.NetworkMode) == 0 {
			p.NetworkMode = "bridge"
		}

		if len(p.HealthCheckCommand) != 0 {
			healthcheck := ecs.HealthCheck{
				Command:  aws.StringSlice(p.HealthCheckCommand),
				Interval: &p.HealthCheckInterval,
				Retries:  &p.HealthCheckRetries,
				Timeout:  &p.HealthCheckTimeout,
			}
			if p.HealthCheckStartPeriod != 0 {
				healthcheck.StartPeriod = &p.HealthCheckStartPeriod
			}
			definition.HealthCheck = &healthcheck
		}
		definitions = append(definitions, definition)
	}

	params := &ecs.RegisterTaskDefinitionInput{
		ContainerDefinitions: definitions,
		Family:               currentTaskDefinition.Family,
		Volumes:              currentTaskDefinition.Volumes,
		TaskRoleArn:          currentTaskDefinition.TaskRoleArn,
		NetworkMode:          currentTaskDefinition.NetworkMode,
	}

	cleanedCompatibilities := strings.Trim(p.Compatibilities, " ")
	compatibilitySlice := strings.Split(cleanedCompatibilities, " ")

	if cleanedCompatibilities != "" && len(compatibilitySlice) != 0 {
		params.RequiresCompatibilities = aws.StringSlice(compatibilitySlice)
	}

	if len(p.TaskCPU) != 0 {
		params.Cpu = aws.String(p.TaskCPU)
	}

	if len(p.TaskMemory) != 0 {
		params.Memory = aws.String(p.TaskMemory)
	}

	if len(p.TaskExecutionRoleArn) != 0 {
		params.ExecutionRoleArn = aws.String(p.TaskExecutionRoleArn)
	}

	resp, err := svc.RegisterTaskDefinition(params)
	if err != nil {
		return err
	}

	newTaskDefinitionArn := *(resp.TaskDefinition.TaskDefinitionArn)
	sparams := &ecs.UpdateServiceInput{
		Cluster:              aws.String(p.Cluster),
		Service:              aws.String(p.Service),
		TaskDefinition:       aws.String(newTaskDefinitionArn),
		NetworkConfiguration: p.setupServiceNetworkConfiguration(),
	}

	if p.DesiredCount >= 0 {
		sparams.DesiredCount = aws.Int64(p.DesiredCount)
	}

	cleanedDeploymentConfiguration := strings.Trim(p.DeploymentConfiguration, " ")
	parts := strings.SplitN(cleanedDeploymentConfiguration, " ", 2)
	minimumHealthyPercent, minimumHealthyPercentError := strconv.ParseInt(parts[0], 10, 64)
	if minimumHealthyPercentError != nil {
		minimumHealthyPercentWrappedErr := errors.New(minimumHealthyPercentBaseParseErr + minimumHealthyPercentError.Error())
		fmt.Println(minimumHealthyPercentWrappedErr.Error())
		return minimumHealthyPercentWrappedErr
	}
	maximumPercent, maximumPercentErr := strconv.ParseInt(parts[1], 10, 64)
	if maximumPercentErr != nil {
		maximumPercentWrappedErr := errors.New(maximumPercentBaseParseErr + maximumPercentErr.Error())
		fmt.Println(maximumPercentWrappedErr.Error())
		return maximumPercentWrappedErr
	}

	sparams.DeploymentConfiguration = &ecs.DeploymentConfiguration{
		MaximumPercent:        aws.Int64(maximumPercent),
		MinimumHealthyPercent: aws.Int64(minimumHealthyPercent),
	}

	_, serr := svc.UpdateService(sparams)
	if serr != nil {
		return serr
	}

	for {
		describeNewServicesOutput, _ := DescribeServices(svc, p.Cluster, p.Service)
		taskSets := describeNewServicesOutput.Services[0].TaskSets
		var newTaskSet *ecs.TaskSet
		for _, taskSet := range taskSets {
			if *taskSet.TaskDefinition == newTaskDefinitionArn {
				newTaskSet = taskSet
				break
			}
		}
		if newTaskSet == nil {
			time.Sleep(5 * time.Second)
			continue
		}
		runningCount := *newTaskSet.RunningCount
		desiredCount := *newTaskSet.ComputedDesiredCount
		fmt.Printf("Task Definition Arn: %s\n", newTaskDefinitionArn)
		fmt.Printf("Running Count: %d\n", runningCount)
		fmt.Printf("Desired Count: %d\n", desiredCount)
		if runningCount == desiredCount {
			fmt.Println("Deployment done.")
			break
		} else {
			time.Sleep(10 * time.Second)
			fmt.Println("Sleeping for 10 seconds...")
		}
	}

	// fmt.Println(sresp)
	// fmt.Println(resp)
	return nil
}

// setupServiceNetworkConfiguration is used to setup the ECS service network
// configuration based on operator input.
func (p *Plugin) setupServiceNetworkConfiguration() *ecs.NetworkConfiguration {
	netConfig := ecs.NetworkConfiguration{AwsvpcConfiguration: &ecs.AwsVpcConfiguration{}}

	if p.NetworkMode != ecs.NetworkModeAwsvpc {
		return &netConfig
	}

	if len(p.ServiceNetworkAssignPublicIp) != 0 {
		netConfig.AwsvpcConfiguration.SetAssignPublicIp(p.ServiceNetworkAssignPublicIp)
	}

	if len(p.ServiceNetworkSubnets) > 0 {
		netConfig.AwsvpcConfiguration.SetSubnets(aws.StringSlice(p.ServiceNetworkSubnets))
	}

	if len(p.ServiceNetworkSecurityGroups) > 0 {
		netConfig.AwsvpcConfiguration.SetSecurityGroups(aws.StringSlice(p.ServiceNetworkSecurityGroups))
	}

	return &netConfig
}

// getOrCreateContainerDefinition retrieves a Container Definition from Task Definition td
// if it exists, otherwise initializes a new one.
func (p *Plugin) getOrCreateContainerDefinition(td *ecs.TaskDefinition, cn string, image string) *ecs.ContainerDefinition {
	for _, containerDefinition := range td.ContainerDefinitions {
		if *containerDefinition.Name == cn {
			containerDefinition.Image = aws.String(image)
			return containerDefinition
		}
	}
	return &ecs.ContainerDefinition{
		Command: []*string{},

		DnsSearchDomains:      []*string{},
		DnsServers:            []*string{},
		DockerLabels:          map[string]*string{},
		DockerSecurityOptions: []*string{},
		EntryPoint:            []*string{},
		Environment:           []*ecs.KeyValuePair{},
		Essential:             aws.Bool(true),
		ExtraHosts:            []*ecs.HostEntry{},

		Image:        aws.String(image),
		Links:        []*string{},
		MountPoints:  []*ecs.MountPoint{},
		Name:         aws.String(cn),
		PortMappings: []*ecs.PortMapping{},

		Ulimits: []*ecs.Ulimit{},
		//User: aws.String("String"),
		VolumesFrom: []*ecs.VolumeFrom{},
		//WorkingDirectory: aws.String("String"),
	}
}
