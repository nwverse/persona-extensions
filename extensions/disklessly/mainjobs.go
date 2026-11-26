package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The 硬盘空间 page's scans and cleanups run as background jobs like the cold-session ones: a
// scan may take 90 seconds and a cleanup minutes, while a UI request has about 12.

// startScan scans machine id in the background.
func (a *app) startScan(id string) (bool, coldJob, error) {
	runner := a.run()
	return a.startJob(id, jobScan, "正在检查硬盘（只看不删）", func(ctx context.Context, sc *scratch, now time.Time) (string, error) {
		s, err := ScanMachine(ctx, runner, sc.st, sc.m, now)
		if err != nil {
			return "", err
		}
		if s.Partial {
			return fmt.Sprintf("检查完了：系统盘用了 %.0f%%；缓存太多，%d 秒内没算完，能腾出多少只算了一部分", usedPercent(s), int(probeTimeout/time.Second)), nil
		}
		return fmt.Sprintf("检查完了：系统盘用了 %.0f%%，可以腾出 %s，什么都没删", usedPercent(s), FormatBytes(s.ReclaimableBytes)), nil
	})
}

func usedPercent(s *Scan) float64 {
	if s == nil || s.Mount.TotalBytes == 0 {
		return 0
	}
	return float64(s.Mount.UsedBytes) * 100 / float64(s.Mount.TotalBytes)
}

// mainJob starts the scan or cleanup that action r asks for and returns the toast.
func (a *app) mainJob(r *ext.UIRequest, p *page) (string, error) {
	runner := a.run()
	if r.Action == "scan-all" {
		type target struct{ id, name string }
		var targets []target
		if err := a.db().read(func(st *State) {
			for _, m := range st.Machines {
				if m.Enabled {
					targets = append(targets, target{m.ID, machineLabel(m)})
				}
			}
		}); err != nil {
			return "", err
		}
		var started, busy []string
		for _, t := range targets {
			ok, _, err := a.startScan(t.id)
			switch {
			case err != nil:
				busy = append(busy, t.name+"（"+userMessage(err)+"）")
			case ok:
				started = append(started, t.name)
			default:
				busy = append(busy, t.name)
			}
		}
		if len(started) == 0 {
			if len(busy) == 0 {
				return "还没有电脑", nil
			}
			return "这些电脑正在忙别的：" + clip(strings.Join(busy, "、"), 80) + "，稍等，页面会自动更新", nil
		}
		toast := fmt.Sprintf("开始检查 %d 台电脑（只看不删），完成后页面会自动更新", len(started))
		if len(busy) > 0 {
			toast += "；" + clip(strings.Join(busy, "、"), 60) + " 正在忙别的"
		}
		return toast, nil
	}

	targetID := r.Values["m"]
	if targetID == "" {
		targetID = p.Machine
	}
	if targetID == "" {
		targetID = "local"
	}
	var id, name string
	if err := a.db().read(func(st *State) {
		if m := st.machine(targetID); m != nil {
			id, name = m.ID, machineLabel(m)
		}
	}); err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("找不到电脑 %q", targetID)
	}
	p.Machine = id

	if r.Action == "scan" {
		ok, cur, err := a.startScan(id)
		if err != nil {
			return "", err
		}
		if !ok {
			return busyNote(name, cur), nil
		}
		return "开始检查「" + name + "」（只看不删），完成后页面会自动更新", nil
	}

	var cats, items []string
	includeReview := false
	label := "正在删掉放心删的缓存"
	switch r.Action {
	case "clean-category":
		c := strings.TrimSpace(r.Values["cat"])
		if c == "" {
			return "", fmt.Errorf("没有指定要删的种类")
		}
		cats, includeReview = []string{c}, true
		label = "正在删「" + categoryName(c) + "」"
	case "clean-item":
		it := strings.TrimSpace(r.Values["item"])
		if it == "" {
			return "", fmt.Errorf("没有指定要删哪一项")
		}
		items, includeReview = []string{it}, true
		label = "正在删一项"
	case "clean-selected":
		for _, cid := range CategoryIDs {
			if r.Values["cat_"+cid] == "true" {
				cats = append(cats, cid)
			}
		}
		if len(cats) == 0 {
			return "", fmt.Errorf("请至少勾选一种要删的")
		}
		includeReview = r.Values["include_review"] == "true"
		label = fmt.Sprintf("正在删勾选的 %d 种", len(cats))
	}
	ok, cur, err := a.startJob(id, jobClean, label, func(ctx context.Context, sc *scratch, now time.Time) (string, error) {
		_, rec, err := ExecuteClean(ctx, runner, sc.st, sc.m, cats, items, includeReview, false, nil, now)
		if err != nil {
			return "", err
		}
		return "删完了，剩余空间 " + rec.Summary, nil
	})
	if err != nil {
		return "", err
	}
	if !ok {
		return busyNote(name, cur), nil
	}
	return "开始在「" + name + "」上删，结果会显示在这台电脑下面，并记进清理记录", nil
}

// busy reports a job running on machine m.
func (d draw) busy(m *Machine) bool {
	j, ok := d.jobs[m.ID]
	return ok && j.running()
}

// machineJobItems draws, under a machine on the 硬盘空间 page, its running job, how its last
// scan or cleanup ended and, after a failed probe, the problem with its command and 详情.
// rescan and reclean name the buttons the caller draws next to it that check again and clean
// again, so a failure's 「重试」 points at a button that is really there.
func (d draw) machineJobItems(m *Machine, rescan, reclean retryHint) []wire.Component {
	var items []wire.Component
	j, hasJob := d.jobs[m.ID]
	if hasJob && j.running() {
		return []wire.Component{{Type: wire.ComponentText, Style: wire.TextStyleSecondary,
			Text: fmt.Sprintf("%s…（%s开始，完成后页面会自动更新）", j.Label, d.when(j.Started))}}
	}
	var raw string
	if hasJob && (j.Kind == jobScan || j.Kind == jobClean) {
		if j.Err != "" && m.LastError != "" && j.Kind == jobScan {
			// The failed scan is the problem drawn below; say it once.
		} else {
			retry := rescan
			if j.Kind == jobClean {
				retry = reclean
			}
			items = append(items, d.jobOutcome(j, retry)...)
			raw = j.Raw
		}
	}
	if m.LastError != "" {
		p := sshProblem(m, m.LastError).renamed(rescan)
		if sandboxed() && p.Cause == causeSandbox {
			// The banner at the top already says it and gives the command.
			items = append(items, wire.Component{Type: wire.ComponentText, Style: wire.TextStyleSecondary, Text: clip("上次检查没有成功："+p.Reason, 300)})
		} else {
			// What is wrong and what to do first, then the command as a secondary step.
			items = append(items, wire.Component{Type: wire.ComponentText, Text: clip("上次检查没有成功："+p.lead(), 600)})
			items = append(items, commandItems(detailsKey("scan", m.ID), p.CmdIntro, p.Command)...)
		}
		raw = strings.TrimSpace(p.Raw + "\n" + raw)
	}
	return append(items, d.detailsItems(detailsKey("scan", m.ID), raw)...)
}
