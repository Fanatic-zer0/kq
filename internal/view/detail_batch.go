package view

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Fanatic-zer0/kq/internal/model"
)

func renderJobDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "Job", target.Ref.Name)
	field(w, "Namespace", target.Ref.Namespace)
	details, _ := detailsOf[*model.JobDetails](target)
	if details == nil {
		return
	}
	status := jobStatus(details)
	statusField(w, "Status", status)
	field(w, "Active", fmt.Sprintf("%d", details.Active))
	field(w, "Completions", fmt.Sprintf("%d/%d", details.Succeeded, details.Completions))
	field(w, "Failed", fmt.Sprintf("%d", details.Failed))
	field(w, "Parallelism", fmt.Sprintf("%d", details.Parallelism))
	field(w, "Backoff Limit", fmt.Sprintf("%d", details.BackoffLimit))
	field(w, "Age", Age(details.CreationTimestamp))
	if details.StartTime != nil {
		field(w, "Started", details.StartTime.Format(time.RFC3339))
	}
	if details.CompletionTime != nil {
		field(w, "Completed", details.CompletionTime.Format(time.RFC3339))
	}
	renderTemplateContainers(w, details.Containers)

	if chain := ownerChain(result.Graph, target); len(chain) > 1 {
		section(w, "OWNED BY")
		writeOwnerTree(w, chain)
	}
	pods := filterByKind(edgeSources(result.Graph, target.Ref.UID, model.RelationOwner), "Pod")
	if len(pods) > 0 {
		section(w, "RELATED RESOURCES")
	}
	renderPodsForOwners(w, result, []model.ResourceNode{target})
	renderChildEvents(w, result, pods)
}

func renderCronJobDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "CronJob", target.Ref.Name)
	field(w, "Namespace", target.Ref.Namespace)
	details, _ := detailsOf[*model.CronJobDetails](target)
	if details == nil {
		return
	}
	field(w, "Schedule", orDash(details.Schedule))
	status := "Enabled"
	if details.Suspend {
		status = "Suspended"
	}
	statusField(w, "Status", status)
	field(w, "Concurrency Policy", orDash(details.ConcurrencyPolicy))
	field(w, "Age", Age(details.CreationTimestamp))
	if details.LastScheduleTime != nil {
		field(w, "Last Scheduled", details.LastScheduleTime.Format(time.RFC3339))
	}
	if details.NextRunTime != nil {
		field(w, "Next Run", details.NextRunTime.Format(time.RFC3339))
		field(w, "Next Run In", FormatDuration(time.Until(*details.NextRunTime)))
	} else if details.ScheduleError != "" {
		field(w, "Next Run", "unavailable: "+details.ScheduleError)
	} else if details.Suspend {
		field(w, "Next Run", "suspended")
	}
	renderTemplateContainers(w, details.Containers)
	if chain := ownerChain(result.Graph, target); len(chain) > 1 {
		section(w, "OWNED BY")
		writeOwnerTree(w, chain)
	}

	jobs := filterByKind(edgeSources(result.Graph, target.Ref.UID, model.RelationOwner), "Job")
	if len(jobs) > 0 {
		section(w, "RELATED RESOURCES")
		fmt.Fprintln(w, "JOBS")
		table := NewTable("NAME", "ACTIVE", "SUCCEEDED", "FAILED", "AGE", "STATUS")
		for _, job := range jobs {
			jobDetails, _ := detailsOf[*model.JobDetails](job)
			if jobDetails == nil {
				table.Row(job.Ref.Name, "-", "-", "-", "-", "UNKNOWN")
				continue
			}
			table.Row(job.Ref.Name, fmt.Sprintf("%d", jobDetails.Active), fmt.Sprintf("%d", jobDetails.Succeeded), fmt.Sprintf("%d", jobDetails.Failed), Age(jobDetails.CreationTimestamp), jobStatus(jobDetails))
		}
		table.Flush(w)
	}

	var pods []model.ResourceNode
	for _, job := range jobs {
		pods = append(pods, filterByKind(edgeSources(result.Graph, job.Ref.UID, model.RelationOwner), "Pod")...)
	}
	renderPodsForOwners(w, result, jobs)
	renderChildEvents(w, result, append(append([]model.ResourceNode{}, jobs...), pods...))
}

func renderTemplateContainers(w io.Writer, containers []model.WorkloadContainerDetails) {
	if len(containers) == 0 {
		return
	}
	section(w, "POD TEMPLATE CONTAINERS")
	for _, container := range containers {
		fmt.Fprintf(w, "%s/%s\n", orDash(container.Type), container.Name)
		fmt.Fprintf(w, "  Image: %s\n", orDash(container.Image))
		fmt.Fprintf(w, "  Command: %s\n", formatCommandLine(container.Command))
		if len(container.Args) > 0 {
			fmt.Fprintln(w, "  Args:")
			for index, arg := range container.Args {
				fmt.Fprintf(w, "    [%d] %s\n", index+1, formatCommandArg(arg))
			}
		} else {
			fmt.Fprintln(w, "  Args: -")
		}
	}
}

func formatCommandLine(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	formatted := make([]string, len(values))
	for i, value := range values {
		formatted[i] = formatCommandArg(value)
	}
	return strings.Join(formatted, " ")
}

func formatCommandArg(value string) string {
	if value == "" || strings.ContainsAny(value, " \t\r\n\"'\\") {
		return strconv.Quote(value)
	}
	return value
}

func jobStatus(details *model.JobDetails) string {
	switch {
	case details.Failed > 0 && details.Active == 0 && details.Succeeded == 0:
		return "Failed"
	case details.Completions > 0 && details.Succeeded >= details.Completions:
		return "Complete"
	case details.Active > 0:
		return "Running"
	default:
		return "Pending"
	}
}

func renderChildEvents(w io.Writer, result *model.QueryResult, nodes []model.ResourceNode) {
	for _, node := range nodes {
		events := eventsForUID(result.Graph, node.Ref.UID)
		if len(events) == 0 {
			continue
		}
		section(w, "EVENTS: "+node.Ref.Kind+"/"+node.Ref.Name)
		for _, event := range events {
			fmt.Fprintf(w, "%s  %-7s  %-10s %s\n", event.LastTimestamp.Format("15:04:05"), eventTypeColor(event.Type), event.Reason, event.Message)
		}
	}
}
