package main

import (
	"os"

	"k8s.io/component-base/cli"
	_ "k8s.io/component-base/logs/json/register"
	_ "k8s.io/component-base/metrics/prometheus/clientgo"
	_ "k8s.io/component-base/metrics/prometheus/version"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"

	"schedulab/pkg/plugins/schedulab"
)

func main() {
	command := app.NewSchedulerCommand(app.WithPlugin(schedulab.Name, schedulab.New))
	os.Exit(cli.Run(command))
}
