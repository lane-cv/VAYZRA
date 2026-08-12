package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	errBusy                = errors.New("update busy")
	errDirty               = errors.New("update checkout dirty")
	errRollbackUnavailable = errors.New("rollback unavailable")
)

type releaseDiscoverer interface {
	latest(context.Context, githubRepository) (stableRelease, error)
	verifyCommit(context.Context, githubRepository, string, string) error
}

type statusPersistence interface {
	load() (updateStatus, bool)
	save(updateStatus) error
}

type operationPersistence interface {
	load() (operationJournal, bool, error)
	save(operationJournal) error
	remove() error
}

type checkoutSnapshot struct {
	Commit        string
	Dirty         bool
	BranchMatches bool
}

type agentActions struct {
	inspect               func(context.Context) (inspection, error)
	createWorktree        func(context.Context, string) (string, error)
	removeWorktree        func(string)
	verifyCandidateSource func(context.Context, string, string) error
	verifyBuildInputs     func(string) error
	captureRuntimeImages  func(context.Context) (runtimeImages, error)
	buildImage            func(context.Context, string, string, string) (string, error)
	verifyCheckout        func(context.Context, string) error
	checkoutSnapshot      func(context.Context) (checkoutSnapshot, error)
	switchRuntime         func(context.Context, string, runtimeImages) error
	mergeCandidate        func(context.Context, string) error
}

type agent struct {
	cfg      config
	mu       sync.Mutex
	status   updateStatus
	store    statusPersistence
	journal  operationPersistence
	releases releaseDiscoverer
	actions  agentActions
	pending  bool
}

type inspection struct {
	status       updateStatus
	release      stableRelease
	candidateRef string
}

type runtimeImages struct {
	app    string
	worker string
}

func newAgent(cfg config) (*agent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	return newAgentWithComponents(
		ctx,
		cfg,
		statusStore{directory: cfg.stateDirectory},
		operationStore{directory: cfg.stateDirectory},
		newGitHubReleaseClient(cfg.githubToken),
		defaultAgentActions,
	)
}

func newAgentWithComponents(
	ctx context.Context,
	cfg config,
	store statusPersistence,
	journal operationPersistence,
	releases releaseDiscoverer,
	actions func(*agent) agentActions,
) (*agent, error) {
	if ctx == nil || store == nil || journal == nil || actions == nil {
		return nil, errors.New("invalid update agent components")
	}
	status := initialStatus(cfg)
	persisted, statusLoaded := store.load()
	if statusLoaded {
		status = persisted
	}
	a := &agent{
		cfg:      cfg,
		status:   status,
		store:    store,
		journal:  journal,
		releases: releases,
	}
	a.actions = actions(a)
	operation, operationLoaded, err := journal.load()
	if err != nil {
		return nil, err
	}
	if operationLoaded {
		if !statusLoaded {
			a.status = recoveryStatusFromOperation(cfg, operation, time.Now().UTC())
		}
		a.pending = true
		if err := a.reconcileOperation(ctx, operation); err != nil {
			return nil, err
		}
		return a, nil
	}
	status = recoverPersistedStatus(status, cfg, time.Now().UTC())
	a.status = status
	if err := store.save(a.status); err != nil {
		return nil, err
	}
	return a, nil
}

func recoveryStatusFromOperation(cfg config, operation operationJournal, started time.Time) updateStatus {
	status := initialStatus(cfg)
	started = started.UTC()
	status.State = stateUpdating
	status.CurrentCommit = operation.CurrentCommit
	status.LatestCommit = operation.CandidateCommit
	status.UpdateAvailable = true
	status.Phase = phaseRecovering
	status.Progress = 1
	status.Message = "检测到未完成更新，正在根据私有恢复日志核对运行态"
	status.StartedAt = &started
	return status
}

func defaultAgentActions(a *agent) agentActions {
	return agentActions{
		inspect:               a.inspect,
		createWorktree:        a.createWorktree,
		removeWorktree:        a.removeWorktree,
		verifyCandidateSource: a.assertWorktreeCommit,
		verifyBuildInputs:     validateCandidateBuildInputs,
		captureRuntimeImages:  a.captureRuntimeImages,
		buildImage:            a.dockerBuildImage,
		verifyCheckout:        a.assertCheckoutUnchanged,
		checkoutSnapshot:      a.currentCheckoutSnapshot,
		switchRuntime: func(ctx context.Context, source string, images runtimeImages) error {
			return a.composeAt(ctx, source, images, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "300", "app", "worker")
		},
		mergeCandidate: func(ctx context.Context, ref string) error {
			_, err := a.git(ctx, "merge", "--ff-only", ref)
			return err
		},
	}
}

func (a *agent) currentCheckoutSnapshot(ctx context.Context) (checkoutSnapshot, error) {
	commit, err := a.git(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil || !validCommit(commit) {
		return checkoutSnapshot{}, errors.New("checkout commit unavailable")
	}
	branch, branchErr := a.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	dirtyOutput, err := a.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return checkoutSnapshot{}, errors.New("checkout status unavailable")
	}
	return checkoutSnapshot{
		Commit:        commit,
		Dirty:         strings.TrimSpace(dirtyOutput) != "",
		BranchMatches: branchErr == nil && branch == a.cfg.ref,
	}, nil
}

func (a *agent) reconcileOperation(ctx context.Context, operation operationJournal) error {
	if !validOperationJournal(operation) || a.actions.checkoutSnapshot == nil || a.actions.switchRuntime == nil {
		return errors.New("operation recovery is unavailable")
	}
	checkout, err := a.actions.checkoutSnapshot(ctx)
	if err != nil {
		return err
	}
	if checkout.Dirty || !checkout.BranchMatches ||
		(checkout.Commit != operation.CurrentCommit && checkout.Commit != operation.CandidateCommit) {
		return a.persistBlockedRecovery("更新中断后的检出状态无法安全自动恢复，请人工核对运行镜像与 Git 检出")
	}
	if checkout.Commit == operation.CurrentCommit {
		if err := a.actions.switchRuntime(ctx, a.cfg.repository, operation.OldImages); err != nil {
			return err
		}
		if err := a.transition(func(status *updateStatus) {
			finished := time.Now().UTC()
			status.State = stateFailed
			status.Phase = phaseFailed
			status.Message = "更新代理重启中断了更新，已自动恢复旧运行态"
			status.CurrentCommit = operation.CurrentCommit
			status.UpdateAvailable = true
			status.FinishedAt = &finished
		}); err != nil {
			return err
		}
		return a.removeOperation()
	}
	if err := a.actions.switchRuntime(ctx, a.cfg.repository, operation.CandidateImages); err != nil {
		return err
	}
	if err := a.transition(func(status *updateStatus) {
		finished := time.Now().UTC()
		previousVersion := status.CurrentVersion
		if status.State == stateSuccess && status.CurrentCommit == operation.CandidateCommit {
			previousVersion = status.PreviousVersion
		}
		status.State = stateSuccess
		status.CurrentVersion = status.LatestVersion
		status.CurrentCommit = operation.CandidateCommit
		status.LatestCommit = operation.CandidateCommit
		status.UpdateAvailable = false
		status.Dirty = false
		status.PreviousVersion = previousVersion
		status.Phase = phaseComplete
		status.Progress = 100
		status.Message = "更新完成，App 与 Worker 已通过重启恢复验证"
		status.FinishedAt = &finished
	}); err != nil {
		return err
	}
	return a.removeOperation()
}

func (a *agent) persistBlockedRecovery(message string) error {
	return a.transition(func(status *updateStatus) {
		finished := time.Now().UTC()
		status.State = stateBlocked
		status.Phase = phaseComplete
		status.Progress = 100
		status.UpdateAvailable = false
		status.Message = sanitizeText(message, 512, false)
		status.FinishedAt = &finished
	})
}

func (a *agent) snapshot() updateStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

func (a *agent) transition(change func(*updateStatus)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.transitionLocked(change)
}

func (a *agent) transitionLocked(change func(*updateStatus)) error {
	previous := a.status
	change(&a.status)
	a.status.Enabled = true
	a.status.Strategy = strategyGitHubRelease
	a.status.Channel = channelStable
	a.status.Ref = a.cfg.ref
	a.status.CanRollback = false
	if err := a.store.save(a.status); err != nil {
		a.status = previous
		return err
	}
	return nil
}

func (a *agent) replace(status updateStatus) error {
	return a.transition(func(current *updateStatus) {
		*current = status
	})
}

func (a *agent) check(ctx context.Context) (updateStatus, error) {
	started := time.Now().UTC()
	previousVersion, err := a.reserveOperation(stateChecking, phaseChecking, 5, "正在检查 GitHub stable Release", started, false)
	if err != nil {
		return updateStatus{}, err
	}

	inspected, err := a.inspect(ctx)
	if err != nil {
		a.setFailure("检查 GitHub Release 失败，请确认仓库、网络与只读 Token 配置", false)
		return updateStatus{}, err
	}
	finished := time.Now().UTC()
	inspected.status.StartedAt = &started
	inspected.status.FinishedAt = &finished
	inspected.status.PreviousVersion = previousVersion
	if err := a.replace(inspected.status); err != nil {
		return updateStatus{}, err
	}
	return a.snapshot(), nil
}

func (a *agent) inspect(ctx context.Context) (inspection, error) {
	if a.releases == nil {
		return inspection{}, errors.New("release discovery unavailable")
	}
	current, err := a.git(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil || !validCommit(current) {
		return inspection{}, errors.New("current commit unavailable")
	}
	branch, branchErr := a.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	remote, repository, err := a.remote(ctx)
	if err != nil {
		return inspection{}, err
	}
	dirtyOutput, err := a.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return inspection{}, err
	}
	dirty := strings.TrimSpace(dirtyOutput) != ""
	currentVersion := a.versionAtCommit(ctx, current)
	release, err := a.releases.latest(ctx, repository)
	if err != nil {
		return inspection{}, err
	}
	candidateRef := isolatedReleaseRef(release.Tag)
	refspec := releaseRefspec(release.Tag)
	if refspec == "" {
		return inspection{}, errors.New("release tag is invalid")
	}
	existingObject, existingErr := a.git(ctx, "rev-parse", "--verify", candidateRef)
	existingCommit := ""
	if existingErr == nil {
		existingCommit, existingErr = a.git(ctx, "rev-parse", "--verify", candidateRef+"^{commit}")
	}
	if existingErr == nil && (!validCommit(existingObject) || !validCommit(existingCommit)) {
		return inspection{}, errors.New("stored release commit is invalid")
	}
	if existingErr == nil {
		if _, err := a.gitNetwork(ctx, "fetch", "--no-tags", remote, "refs/tags/"+release.Tag); err != nil {
			return inspection{}, errors.New("release tag fetch failed")
		}
		fetchedObject, objectErr := a.git(ctx, "rev-parse", "--verify", "FETCH_HEAD")
		fetchedCommit, commitErr := a.git(ctx, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
		if objectErr != nil || commitErr != nil ||
			!publishedTagUnchanged(existingObject, fetchedObject, existingCommit, fetchedCommit) {
			return inspection{}, errors.New("published release tag moved")
		}
	} else if _, err := a.gitNetwork(ctx, "fetch", "--no-tags", remote, refspec); err != nil {
		return inspection{}, errors.New("release tag fetch failed")
	}
	latest, err := a.git(ctx, "rev-parse", "--verify", candidateRef+"^{commit}")
	if err != nil || !validCommit(latest) {
		return inspection{}, errors.New("release commit unavailable")
	}
	branchRefspec := configuredBranchRefspec(a.cfg.ref)
	if branchRefspec == "" {
		return inspection{}, errors.New("configured branch is invalid")
	}
	if _, err := a.gitNetwork(ctx, "fetch", "--no-tags", remote, branchRefspec); err != nil {
		return inspection{}, errors.New("configured branch fetch failed")
	}
	remoteBranchCommit, err := a.git(ctx, "rev-parse", "--verify", isolatedBranchRef(a.cfg.ref)+"^{commit}")
	if err != nil || !validCommit(remoteBranchCommit) {
		return inspection{}, errors.New("configured branch commit unavailable")
	}
	releaseOnConfiguredBranch, err := a.isAncestor(ctx, latest, remoteBranchCommit)
	if err != nil {
		return inspection{}, err
	}
	if err := a.releases.verifyCommit(ctx, repository, trustedReleaseBranch, latest); err != nil {
		return inspection{}, err
	}
	published := release.PublishedAt
	status := initialStatus(a.cfg)
	status.Repository = repository.canonicalURL()
	status.CurrentVersion = currentVersion
	status.LatestVersion = release.Version
	status.CurrentCommit = current
	status.LatestCommit = latest
	status.ReleaseName = release.Name
	status.ReleaseNotes = release.Notes
	status.ReleaseURL = release.URL
	status.PublishedAt = &published
	status.Dirty = dirty
	status.Phase = phaseComplete
	status.Progress = 100
	status.State = stateCurrent
	status.Message = "当前已是最新 stable Release"

	if current != latest {
		currentIsAncestor, err := a.isAncestor(ctx, current, latest)
		if err != nil {
			return inspection{}, err
		}
		latestIsAncestor, err := a.isAncestor(ctx, latest, current)
		if err != nil {
			return inspection{}, err
		}
		switch {
		case currentIsAncestor:
			status.State = stateAvailable
			status.UpdateAvailable = true
			status.Message = "发现新的 stable Release " + release.Version
		case latestIsAncestor:
			status.State = stateCurrent
			status.UpdateAvailable = false
			status.Message = "当前提交领先于最新 stable Release，不会自动降级"
		default:
			status.State = stateBlocked
			status.UpdateAvailable = false
			status.Message = "当前提交与最新 stable Release 已分叉，无法安全快进"
		}
	}
	if !releaseOnConfiguredBranch {
		status.State = stateBlocked
		status.UpdateAvailable = false
		status.Message = "stable Release 不属于配置的远端分支，已阻止旁支标签更新"
	}
	if status.UpdateAvailable {
		changed, err := a.migrationChanges(ctx, current, latest)
		if err != nil {
			return inspection{}, err
		}
		if changed {
			status.State = stateBlocked
			status.UpdateAvailable = false
			status.Message = "Release 包含数据库 migration，必须使用 Phase 6 正式不可变镜像发布与回滚流程"
		}
	}
	if status.UpdateAvailable {
		changed, err := a.otaControlPlaneChanges(ctx, current, latest)
		if err != nil {
			return inspection{}, err
		}
		if changed {
			status.State = stateBlocked
			status.UpdateAvailable = false
			status.Message = "Release 修改了本地 OTA 控制面，必须在宿主机完整重新部署"
		}
	}
	blockUnsafeCheckout(&status, branchErr == nil && branch == a.cfg.ref, dirty)
	return inspection{status: status, release: release, candidateRef: candidateRef}, nil
}

func publishedTagUnchanged(existingObject, fetchedObject, existingCommit, fetchedCommit string) bool {
	return validCommit(existingObject) && validCommit(fetchedObject) &&
		validCommit(existingCommit) && validCommit(fetchedCommit) &&
		existingObject == fetchedObject && existingCommit == fetchedCommit
}

func (a *agent) apply() (updateStatus, error) {
	now := time.Now().UTC()
	_, err := a.reserveOperation(stateUpdating, phaseChecking, 1, "正在准备 stable Release 更新", now, true)
	if err != nil {
		return updateStatus{}, err
	}
	go a.runUpdate()
	return a.snapshot(), nil
}

func (a *agent) reserveOperation(state, phase string, progress int, message string, started time.Time, rejectDirty bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending || a.status.State == stateChecking || a.status.State == stateUpdating {
		return "", errBusy
	}
	if rejectDirty && a.status.Dirty {
		return "", errDirty
	}
	previousVersion := a.status.PreviousVersion
	if err := a.transitionLocked(func(status *updateStatus) {
		status.State = state
		status.Phase = phase
		status.Progress = progress
		status.Message = message
		started = started.UTC()
		status.StartedAt = &started
		status.FinishedAt = nil
	}); err != nil {
		return "", err
	}
	return previousVersion, nil
}

func (a *agent) rollback(context.Context) (updateStatus, error) {
	return a.snapshot(), errRollbackUnavailable
}

func (a *agent) runUpdate() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if a.actions.inspect == nil || a.actions.createWorktree == nil || a.actions.removeWorktree == nil ||
		a.actions.verifyCandidateSource == nil || a.actions.verifyBuildInputs == nil ||
		a.actions.captureRuntimeImages == nil || a.actions.buildImage == nil ||
		a.actions.verifyCheckout == nil || a.actions.switchRuntime == nil || a.actions.mergeCandidate == nil {
		a.setFailure("更新执行边界不可用", false)
		return
	}
	inspected, err := a.actions.inspect(ctx)
	if err != nil {
		a.setFailure("更新前检查失败", false)
		return
	}
	if err := a.transition(func(status *updateStatus) {
		started := status.StartedAt
		previous := status.PreviousVersion
		*status = inspected.status
		status.State = stateUpdating
		status.Phase = phaseFetching
		status.Progress = 15
		status.Message = "已验证 Release，正在准备隔离工作区"
		status.StartedAt = started
		status.FinishedAt = nil
		status.PreviousVersion = previous
	}); err != nil {
		a.setFailure("更新状态持久化失败，已停止更新", false)
		return
	}
	if inspected.status.Dirty {
		a.setFailure("部署目录有未提交修改，已停止更新", inspected.status.UpdateAvailable)
		return
	}
	if inspected.status.State == stateBlocked {
		a.setFailure(inspected.status.Message, false)
		return
	}
	if !inspected.status.UpdateAvailable {
		a.setComplete("当前提交无需更新，不会执行降级")
		return
	}

	oldImages, err := a.actions.captureRuntimeImages(ctx)
	if err != nil || !validRuntimeImages(oldImages) {
		a.setFailure("无法在构建前捕获旧运行态的不可变镜像 ID，已停止更新", true)
		return
	}
	worktree, err := a.actions.createWorktree(ctx, inspected.status.LatestCommit)
	if err != nil {
		a.setFailure("创建隔离更新工作区失败", true)
		return
	}
	defer a.actions.removeWorktree(worktree)
	if err := a.actions.verifyCandidateSource(ctx, worktree, inspected.status.LatestCommit); err != nil {
		a.setFailure("隔离更新工作区不是已验证的候选提交", true)
		return
	}
	if err := a.actions.verifyBuildInputs(worktree); err != nil {
		_ = a.persistBlockedRecovery("候选 Dockerfile 使用了未钉住 digest 的外部镜像，已阻止在线更新")
		return
	}
	if !a.progress(phaseBuilding, 30, "正在隔离工作区构建 App 镜像") {
		return
	}
	imageTags := runtimeImages{
		app:    a.cfg.project + "-app:update-" + inspected.status.LatestCommit[:12],
		worker: a.cfg.project + "-worker:update-" + inspected.status.LatestCommit[:12],
	}
	images := runtimeImages{}
	images.app, err = a.actions.buildImage(ctx, worktree, "Dockerfile", imageTags.app)
	if err != nil || !validImageID(images.app) {
		a.setFailure("隔离工作区构建 App 镜像失败", true)
		return
	}
	if !a.progress(phaseBuilding, 50, "正在隔离工作区构建 Worker 镜像") {
		return
	}
	images.worker, err = a.actions.buildImage(ctx, worktree, "Dockerfile.worker", imageTags.worker)
	if err != nil || !validImageID(images.worker) {
		a.setFailure("隔离工作区构建 Worker 镜像失败", true)
		return
	}
	if err := a.actions.verifyCheckout(ctx, inspected.status.CurrentCommit); err != nil {
		a.setFailure("构建期间部署目录发生变化，已停止切换", true)
		return
	}
	operation := operationJournal{
		Version:         operationVersion,
		Stage:           operationStagePrepared,
		CurrentCommit:   inspected.status.CurrentCommit,
		CandidateCommit: inspected.status.LatestCommit,
		OldImages:       oldImages,
		CandidateImages: images,
	}
	if err := a.saveOperation(operation); err != nil {
		a.setFailure("无法持久化更新恢复信息，未切换运行态", true)
		return
	}
	if err := a.transition(func(status *updateStatus) {
		status.State = stateUpdating
		status.Phase = phaseSwitching
		status.Progress = 70
		status.Message = "正在切换 App 与 Worker 到新镜像"
	}); err != nil {
		a.failBeforeSwitch("更新状态持久化失败，未切换运行态")
		return
	}
	operation.Stage = operationStageSwitching
	if err := a.saveOperation(operation); err != nil {
		a.failBeforeSwitch("无法持久化切换阶段，未切换运行态")
		return
	}
	if err := a.transition(func(status *updateStatus) {
		status.State = stateUpdating
		status.Phase = phaseVerifying
		status.Progress = 80
		status.Message = "正在切换镜像并等待新版本健康检查"
	}); err != nil {
		a.failBeforeSwitch("更新状态持久化失败，未切换运行态")
		return
	}
	if err := a.actions.switchRuntime(ctx, worktree, images); err != nil {
		a.recoverOldRuntime(operation, "新版本切换或健康检查失败", false)
		return
	}
	operation.Stage = operationStageSwitched
	if err := a.saveOperation(operation); err != nil {
		a.recoverOldRuntime(operation, "无法持久化已切换阶段", false)
		return
	}
	if err := a.actions.verifyCheckout(ctx, inspected.status.CurrentCommit); err != nil {
		a.recoverOldRuntime(operation, "健康检查后部署目录发生变化", true)
		return
	}
	if err := a.transition(func(status *updateStatus) {
		status.State = stateUpdating
		status.Phase = phaseMerging
		status.Progress = 92
		status.Message = "新版本健康，正在快进主检出"
	}); err != nil {
		a.recoverOldRuntime(operation, "更新状态持久化失败", false)
		return
	}
	if err := a.actions.mergeCandidate(ctx, inspected.status.LatestCommit); err != nil {
		a.recoverOldRuntime(operation, "主检出 fast-forward 失败", true)
		return
	}
	if err := a.actions.verifyCheckout(ctx, inspected.status.LatestCommit); err != nil {
		a.recoverOldRuntime(operation, "主检出未落在已验证的候选提交", true)
		return
	}
	operation.Stage = operationStageMerged
	if err := a.saveOperation(operation); err != nil {
		// HEAD already identifies the candidate commit. Keep the earlier durable
		// journal so startup reconciliation can finish without guessing.
		return
	}
	if err := a.transition(func(status *updateStatus) {
		finished := time.Now().UTC()
		started := status.StartedAt
		previousVersion := inspected.status.CurrentVersion
		*status = inspected.status
		status.State = stateSuccess
		status.CurrentVersion = inspected.release.Version
		status.CurrentCommit = inspected.status.LatestCommit
		status.UpdateAvailable = false
		status.Dirty = false
		status.CanRollback = false
		status.PreviousVersion = previousVersion
		status.Phase = phaseComplete
		status.Progress = 100
		status.Message = "更新完成，App 与 Worker 已通过健康检查"
		status.StartedAt = started
		status.FinishedAt = &finished
	}); err != nil {
		// Never rewrite the old commit as a failed terminal state after merge.
		// The durable journal plus HEAD lets startup finalize idempotently.
		return
	}
	_ = a.removeOperation()
}

func (a *agent) progress(phase string, progress int, message string) bool {
	if err := a.transition(func(status *updateStatus) {
		status.State = stateUpdating
		status.Phase = phase
		status.Progress = progress
		status.Message = message
	}); err != nil {
		a.setFailure("更新状态持久化失败，已停止更新", true)
		return false
	}
	return true
}

func (a *agent) persistFailure(message string, updateAvailable bool) error {
	return a.transition(func(status *updateStatus) {
		finished := time.Now().UTC()
		status.State = stateFailed
		status.Phase = phaseFailed
		status.Message = sanitizeText(message, 512, false)
		status.FinishedAt = &finished
		status.UpdateAvailable = updateAvailable
	})
}

func (a *agent) setFailure(message string, updateAvailable bool) {
	_ = a.persistFailure(message, updateAvailable)
}

func (a *agent) setComplete(message string) {
	_ = a.transition(func(status *updateStatus) {
		finished := time.Now().UTC()
		status.State = stateSuccess
		status.Phase = phaseComplete
		status.Progress = 100
		status.Message = sanitizeText(message, 512, false)
		status.FinishedAt = &finished
		status.UpdateAvailable = false
	})
}

func (a *agent) saveOperation(operation operationJournal) error {
	if err := a.journal.save(operation); err != nil {
		_, loaded, loadErr := a.journal.load()
		if loaded || loadErr != nil {
			a.mu.Lock()
			a.pending = true
			a.mu.Unlock()
		}
		return err
	}
	a.mu.Lock()
	a.pending = true
	a.mu.Unlock()
	return nil
}

func (a *agent) removeOperation() error {
	if err := a.journal.remove(); err != nil {
		return err
	}
	a.mu.Lock()
	a.pending = false
	a.mu.Unlock()
	return nil
}

func (a *agent) failBeforeSwitch(reason string) {
	if err := a.persistFailure(reason, true); err == nil {
		_ = a.removeOperation()
	}
}

func (a *agent) recoverOldRuntime(operation operationJournal, reason string, retainJournal bool) {
	_ = a.transition(func(status *updateStatus) {
		status.State = stateUpdating
		status.Phase = phaseRecovering
		status.Progress = 85
		status.Message = sanitizeText(reason+"，正在自动恢复旧运行态", 512, false)
	})
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	if err := a.actions.switchRuntime(recoveryCtx, a.cfg.repository, operation.OldImages); err != nil {
		_ = a.persistFailure(reason+"，自动恢复旧运行态也失败，请立即人工处理", true)
		return
	}
	if retainJournal {
		_ = a.persistBlockedRecovery(reason + "，已恢复旧运行态；检出状态需人工核对")
		return
	}
	if err := a.persistFailure(reason+"，已自动恢复旧运行态", true); err == nil {
		_ = a.removeOperation()
	}
}

func (a *agent) createWorktree(ctx context.Context, ref string) (string, error) {
	root := filepath.Join(a.cfg.stateDirectory, "worktrees")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	path, err := os.MkdirTemp(root, "candidate-")
	if err != nil {
		return "", err
	}
	if !pathWithin(root, path) {
		_ = os.RemoveAll(path)
		return "", errors.New("unsafe worktree path")
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	if _, err := a.git(ctx, "worktree", "add", "--detach", path, ref); err != nil {
		_ = os.RemoveAll(path)
		return "", err
	}
	return path, nil
}

func (a *agent) removeWorktree(path string) {
	root := filepath.Join(a.cfg.stateDirectory, "worktrees")
	if path == "" || !pathWithin(root, path) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _ = a.git(ctx, "worktree", "remove", "--force", path)
	_ = os.RemoveAll(path)
}

func (a *agent) assertWorktreeCommit(ctx context.Context, source, expected string) error {
	root := filepath.Join(a.cfg.stateDirectory, "worktrees")
	if !pathWithin(root, source) || !validCommit(expected) {
		return errors.New("invalid candidate worktree")
	}
	command := exec.CommandContext(ctx, "git", "-c", "safe.directory="+source, "-C", source, "rev-parse", "--verify", "HEAD")
	commit, err := commandOutput(command)
	if err != nil || commit != expected {
		return errors.New("candidate worktree commit changed")
	}
	return nil
}

func (a *agent) dockerBuild(ctx context.Context, source, dockerfile, image string) error {
	file := filepath.Join(source, dockerfile)
	if !pathWithin(source, file) || len(image) > 200 || hasControl(image, false) {
		return errors.New("invalid docker build input")
	}
	if _, err := os.Stat(file); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "docker", "build", "--pull=false", "--tag", image, "--file", file, source)
	command.Dir = source
	return command.Run()
}

func (a *agent) dockerBuildImage(ctx context.Context, source, dockerfile, image string) (string, error) {
	if err := a.dockerBuild(ctx, source, dockerfile, image); err != nil {
		return "", err
	}
	return dockerImageID(ctx, image)
}

func dockerImageID(ctx context.Context, image string) (string, error) {
	if image == "" || len(image) > 200 || hasControl(image, false) {
		return "", errors.New("invalid image")
	}
	command := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image)
	output, err := commandOutput(command)
	if err != nil || !validImageID(output) {
		return "", errors.New("built image ID unavailable")
	}
	return output, nil
}

func (a *agent) captureRuntimeImages(ctx context.Context) (runtimeImages, error) {
	appContainer, err := a.composeOutputAt(ctx, a.cfg.repository, runtimeImages{}, "ps", "-q", "app")
	if err != nil {
		return runtimeImages{}, err
	}
	workerContainer, err := a.composeOutputAt(ctx, a.cfg.repository, runtimeImages{}, "ps", "-q", "worker")
	if err != nil {
		return runtimeImages{}, err
	}
	app, err := dockerContainerImage(ctx, firstLine(appContainer))
	if err != nil {
		return runtimeImages{}, err
	}
	worker, err := dockerContainerImage(ctx, firstLine(workerContainer))
	if err != nil {
		return runtimeImages{}, err
	}
	return runtimeImages{app: app, worker: worker}, nil
}

func dockerContainerImage(ctx context.Context, container string) (string, error) {
	return dockerContainerImageWithOutput(ctx, container, commandOutput)
}

func dockerContainerImageWithOutput(ctx context.Context, container string, outputCommand func(*exec.Cmd) (string, error)) (string, error) {
	if container == "" || len(container) > 128 || hasControl(container, false) {
		return "", errors.New("invalid container")
	}
	if ctx == nil || outputCommand == nil {
		return "", errors.New("container image unavailable")
	}
	command := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Image}}", container)
	output, err := outputCommand(command)
	if err != nil || !validImageID(output) {
		return "", errors.New("container image unavailable")
	}
	return output, nil
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	return strings.TrimSpace(line)
}

func (a *agent) assertCheckoutUnchanged(ctx context.Context, expected string) error {
	current, err := a.git(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil || current != expected {
		return errors.New("checkout changed")
	}
	branch, err := a.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch != a.cfg.ref {
		return errors.New("branch changed")
	}
	dirty, err := a.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil || strings.TrimSpace(dirty) != "" {
		return errors.New("checkout dirty")
	}
	return nil
}

func (a *agent) composeAt(ctx context.Context, source string, images runtimeImages, args ...string) error {
	command, err := a.composeCommand(ctx, source, images, args...)
	if err != nil {
		return err
	}
	return command.Run()
}

func (a *agent) composeOutputAt(ctx context.Context, source string, images runtimeImages, args ...string) (string, error) {
	command, err := a.composeCommand(ctx, source, images, args...)
	if err != nil {
		return "", err
	}
	return commandOutput(command)
}

func (a *agent) composeCommand(ctx context.Context, source string, images runtimeImages, args ...string) (*exec.Cmd, error) {
	source = filepath.Clean(source)
	if source != filepath.Clean(a.cfg.repository) && !pathWithin(filepath.Join(a.cfg.stateDirectory, "worktrees"), source) {
		return nil, errors.New("invalid compose source")
	}
	overrideRelative, err := filepath.Rel(a.cfg.repository, a.cfg.composeOverride)
	if err != nil || filepath.IsAbs(overrideRelative) || strings.HasPrefix(overrideRelative, "..") {
		return nil, errors.New("invalid compose override")
	}
	baseFile := filepath.Join(source, "deploy", "compose.dev.yml")
	overrideFile := filepath.Join(source, overrideRelative)
	for _, file := range []string{baseFile, overrideFile} {
		if !pathWithin(source, file) {
			return nil, errors.New("invalid compose file")
		}
		if _, err := os.Stat(file); err != nil {
			return nil, err
		}
	}
	composeArgs := []string{
		"compose", "--project-name", a.cfg.project,
		"--project-directory", source,
		"--env-file", a.cfg.envFile,
		"--env-file", a.cfg.aiEnvFile,
		"-f", baseFile,
		"-f", overrideFile,
	}
	composeArgs = append(composeArgs, args...)
	command := exec.CommandContext(ctx, "docker", composeArgs...)
	values := map[string]string{
		"HAPPYLEARN_UPDATE_REPOSITORY":       a.cfg.repository,
		"HAPPYLEARN_UPDATE_REF":              a.cfg.ref,
		"HAPPYLEARN_UPDATE_PROJECT":          a.cfg.project,
		"HAPPYLEARN_AISTOR_LICENSE_FILE":     a.cfg.licenseFile,
		"HAPPYLEARN_UPDATE_AGENT_TOKEN_FILE": a.cfg.tokenFile,
	}
	if images.app != "" || images.worker != "" {
		if images.app == "" || images.worker == "" || len(images.app) > 512 || len(images.worker) > 512 ||
			hasControl(images.app, false) || hasControl(images.worker, false) {
			return nil, errors.New("invalid runtime images")
		}
		values["HAPPYLEARN_APP_IMAGE"] = images.app
		values["HAPPYLEARN_WORKER_IMAGE"] = images.worker
	}
	command.Env = overrideEnvironment(os.Environ(), values)
	command.Dir = source
	return command, nil
}

func (a *agent) gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	gitArgs := append([]string{"-c", "safe.directory=" + a.cfg.repository, "-C", a.cfg.repository}, args...)
	return exec.CommandContext(ctx, "git", gitArgs...)
}

func (a *agent) git(ctx context.Context, args ...string) (string, error) {
	return commandOutput(a.gitCommand(ctx, args...))
}

func (a *agent) remote(ctx context.Context) (string, githubRepository, error) {
	raw, err := a.git(ctx, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", githubRepository{}, err
	}
	repository, err := parseGitHubRepository(raw)
	if err != nil {
		return "", githubRepository{}, err
	}
	return repository.canonicalURL(), repository, nil
}

func (a *agent) gitNetworkCommand(ctx context.Context, args ...string) *exec.Cmd {
	command := a.gitCommand(ctx, args...)
	command.Env = gitNetworkEnvironment(os.Environ(), a.cfg.githubToken)
	return command
}

func (a *agent) gitNetwork(ctx context.Context, args ...string) (string, error) {
	return commandOutput(a.gitNetworkCommand(ctx, args...))
}

func (a *agent) isAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	if !validCommit(ancestor) || !validCommit(descendant) {
		return false, errors.New("invalid commit")
	}
	err := a.gitCommand(ctx, "merge-base", "--is-ancestor", ancestor, descendant).Run()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func (a *agent) migrationChanges(ctx context.Context, current, latest string) (bool, error) {
	if !validCommit(current) || !validCommit(latest) {
		return false, errors.New("invalid migration comparison")
	}
	output, err := a.git(ctx, "diff", "--name-status", "--no-renames", current+".."+latest, "--", "db/migrations")
	if err != nil {
		return false, err
	}
	return hasMigrationChanges(output), nil
}

func (a *agent) otaControlPlaneChanges(ctx context.Context, current, latest string) (bool, error) {
	if !validCommit(current) || !validCommit(latest) {
		return false, errors.New("invalid OTA control-plane comparison")
	}
	paths := []string{
		"cmd/update-agent",
		"internal/updates",
		"deploy/Dockerfile.update-agent",
		"deploy/compose.dev.yml",
		"deploy/compose.github.yml",
		"scripts/deploy-from-github.sh",
	}
	arguments := []string{"diff", "--name-status", "--no-renames", current + ".." + latest, "--"}
	arguments = append(arguments, paths...)
	output, err := a.git(ctx, arguments...)
	if err != nil {
		return false, err
	}
	return hasOTAControlPlaneChanges(output), nil
}

func hasMigrationChanges(output string) bool {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		for _, field := range fields[1:] {
			path := strings.TrimPrefix(strings.ReplaceAll(field, "\\", "/"), "./")
			if path == "db/migrations" || strings.HasPrefix(path, "db/migrations/") {
				return true
			}
		}
	}
	return false
}

func hasOTAControlPlaneChanges(output string) bool {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		for _, field := range fields[1:] {
			path := strings.TrimPrefix(strings.ReplaceAll(field, "\\", "/"), "./")
			if path == "cmd/update-agent" || strings.HasPrefix(path, "cmd/update-agent/") ||
				path == "internal/updates" || strings.HasPrefix(path, "internal/updates/") ||
				path == "deploy/Dockerfile.update-agent" || path == "deploy/compose.github.yml" ||
				path == "deploy/compose.dev.yml" ||
				path == "scripts/deploy-from-github.sh" {
				return true
			}
		}
	}
	return false
}

func blockUnsafeCheckout(status *updateStatus, branchMatches, dirty bool) {
	if status == nil {
		return
	}
	if !branchMatches {
		status.State = stateBlocked
		status.UpdateAvailable = false
		status.Message = "部署目录未检出配置的分支，无法自动更新"
	}
	if dirty {
		status.State = stateBlocked
		status.UpdateAvailable = false
		status.Message = "部署目录有未提交修改，无法自动更新"
	}
}

func (a *agent) versionAtCommit(ctx context.Context, commit string) string {
	if !validCommit(commit) {
		return ""
	}
	var selected semanticVersion
	found := false
	if tags, err := a.git(ctx, "tag", "--points-at", commit); err == nil {
		for _, tag := range strings.Fields(tags) {
			if version, ok := parseStableSemanticVersion(tag); ok && (!found || version.compare(selected) > 0) {
				selected, found = version, true
			}
		}
	}
	if refs, err := a.git(ctx, "for-each-ref", "--format=%(refname)", "refs/happylearn-update/releases"); err == nil {
		for _, ref := range strings.Fields(refs) {
			tag := filepath.Base(ref)
			version, ok := parseStableSemanticVersion(tag)
			if !ok {
				continue
			}
			resolved, err := a.git(ctx, "rev-parse", "--verify", ref+"^{commit}")
			if err == nil && resolved == commit && (!found || version.compare(selected) > 0) {
				selected, found = version, true
			}
		}
	}
	if !found {
		return ""
	}
	return selected.normalized
}

func isolatedReleaseRef(tag string) string {
	return "refs/happylearn-update/releases/" + strings.TrimPrefix(tag, "v")
}

func isolatedBranchRef(ref string) string {
	if !validBranchRef(ref) {
		return ""
	}
	return "refs/happylearn-update/branches/" + ref
}

func configuredBranchRefspec(ref string) string {
	isolated := isolatedBranchRef(ref)
	if isolated == "" {
		return ""
	}
	return "refs/heads/" + ref + ":" + isolated
}

func releaseRefspec(tag string) string {
	if _, ok := parseStableSemanticVersion(tag); !ok {
		return ""
	}
	return "refs/tags/" + tag + ":" + isolatedReleaseRef(tag)
}

func githubBasicAuthorization(token string) string {
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return "Authorization: Basic " + credential
}

func gitNetworkEnvironment(base []string, token string) []string {
	result := make([]string, 0, len(base)+24)
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		preserveGitTLS := name == "GIT_SSL_CAINFO" || name == "GIT_SSL_CAPATH"
		if !ok || strings.HasPrefix(name, "GIT_") && !preserveGitTLS || name == "GCM_INTERACTIVE" || strings.HasPrefix(name, "SSH_ASKPASS") {
			continue
		}
		result = append(result, entry)
	}
	result = append(result,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"GCM_INTERACTIVE=never",
	)
	configuration := [][2]string{
		{"credential.helper", ""},
		{"credential.interactive", "false"},
		{"http.extraHeader", ""},
		{"http.https://github.com/.extraHeader", ""},
		{"http.followRedirects", "false"},
		{"protocol.allow", "never"},
		{"protocol.https.allow", "always"},
	}
	if token != "" {
		configuration = append(configuration, [2]string{"http.https://github.com/.extraHeader", githubBasicAuthorization(token)})
	}
	result = append(result, "GIT_CONFIG_COUNT="+fmt.Sprint(len(configuration)))
	for index, item := range configuration {
		result = append(result,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index, item[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, item[1]),
		)
	}
	return result
}

func overrideEnvironment(base []string, values map[string]string) []string {
	result := make([]string, 0, len(base)+len(values))
	seen := make(map[string]bool, len(values))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if value, replace := values[name]; replace {
			if !seen[name] {
				result = append(result, name+"="+value)
				seen[name] = true
			}
			continue
		}
		if ok {
			result = append(result, entry)
		}
	}
	for name, value := range values {
		if !seen[name] {
			result = append(result, name+"="+value)
		}
	}
	return result
}

type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *cappedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.buffer.Write(value)
	}
	return original, nil
}

func commandOutput(command *exec.Cmd) (string, error) {
	stdout := &cappedBuffer{limit: maxOutput}
	stderr := &cappedBuffer{limit: maxOutput}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.buffer.String()), nil
}
