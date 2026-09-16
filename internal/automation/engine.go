package automation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type Engine struct {
	Game         Game
	Store        Store
	PollInterval time.Duration
	Now          func() time.Time
}

type Preflight struct {
	Ready           bool     `json:"ready"`
	Problems        []string `json:"problems"`
	Budget          Budget   `json:"budget"`
	AlreadyComplete bool     `json:"already_complete"`
}

// EntryPreconditions returns the guards for the first stage which still needs
// to run. A completed descendant selects its reviewed ancestors as skipped;
// guards belonging to the selected stage remain explicit. The result is
// always a copy owned by the caller.
func (p Plan) EntryPreconditions(o Observation) []Condition {
	if len(p.Stages) == 0 {
		return append([]Condition(nil), p.Preconditions...)
	}
	index := p.firstIncompleteStage(o)
	if index >= len(p.Stages) {
		return nil
	}
	return p.stageEntryConditions(index)
}

// completedStageClosure marks stages whose completion is currently observed
// and all of their reviewed ancestors. It is used only while a stage is not
// entered. Once execution has entered a stage, its persisted checkpoint is
// the source of truth and a later descendant cannot skip it.
func (p Plan) completedStageClosure(o Observation) []bool {
	done := make([]bool, len(p.Stages))
	byID := make(map[string]int, len(p.Stages))
	for i, stage := range p.Stages {
		byID[stage.ID] = i
		if len(stage.Completion) > 0 && conditionsMatch(stage.Completion, o) {
			done[i] = true
		}
	}
	changed := true
	for changed {
		changed = false
		for i, stage := range p.Stages {
			if !done[i] {
				continue
			}
			for _, dependencyID := range stage.Dependencies {
				dependency, ok := byID[dependencyID]
				if ok && !done[dependency] {
					done[dependency] = true
					changed = true
				}
			}
		}
	}
	return done
}

func (p Plan) firstIncompleteStage(o Observation) int {
	done := p.completedStageClosure(o)
	for i, complete := range done {
		if !complete {
			return i
		}
	}
	return len(p.Stages)
}

// stageEntryConditions copies a stage's guards. Dependency completion guards
// remain explicit entry requirements: a checkpoint proves that a prerequisite
// ran in the past, but it does not prove that a later branch still has the
// item or other state required to start this stage.
func (p Plan) stageEntryConditions(index int) []Condition {
	if index < 0 || index >= len(p.Stages) {
		return nil
	}
	result := make([]Condition, 0, len(p.Preconditions)+len(p.Stages[index].Preconditions))
	result = append(result, p.Preconditions...)
	result = append(result, p.Stages[index].Preconditions...)
	return result
}

func conditionsEqual(left, right []Condition) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (p Plan) Validate() error {
	if p.ID == "" || p.CharacterID == "" || p.KnowledgeRevision == "" {
		return errors.New("plan identity and knowledge revision required")
	}
	if p.Mode != "quest" && p.Mode != "leveling" {
		return errors.New("unsupported automation mode")
	}
	if p.MaximumSeconds <= 0 || p.MaximumSeconds > 30*24*60*60 || p.MaximumDeaths < 0 {
		return errors.New("invalid execution limits")
	}
	b := p.Budget
	if b.Minimum < 0 || b.ExpectedLow < 0 || b.ExpectedHigh < b.ExpectedLow || b.Reserve < 0 || b.MaximumSpend < 0 {
		return errors.New("invalid budget")
	}
	if p.Mode == "quest" && len(p.Completion) == 0 {
		return errors.New("quest requires authoritative completion conditions")
	}
	if p.Mode == "leveling" {
		if len(p.Targets) == 0 || (p.TargetPolicy != "all" && p.TargetPolicy != "any") {
			return errors.New("level targets and all/any policy required")
		}
		seen := map[string]bool{}
		for _, t := range p.Targets {
			if (t.Kind != "character" && t.Kind != "pet") || t.Level <= 0 || t.Level > 1000 || (t.Kind == "pet" && t.ID == "") {
				return errors.New("invalid level target")
			}
			key := t.Kind + ":" + t.ID
			if seen[key] {
				return errors.New("duplicate level target")
			}
			seen[key] = true
		}
	}
	if len(p.Steps) == 0 {
		return errors.New("plan has no executable steps")
	}
	if len(p.Stages) > 0 {
		if p.Mode != "quest" {
			return errors.New("stages are supported only for quest plans")
		}
		stageIDs := make(map[string]bool, len(p.Stages))
		previousEnd := 0
		for _, stage := range p.Stages {
			if stage.ID == "" || stageIDs[stage.ID] {
				return errors.New("invalid or duplicate quest stage ID")
			}
			if stage.StartStep != previousEnd || stage.StartStep < 0 || stage.EndStep <= stage.StartStep || stage.EndStep > len(p.Steps) {
				return errors.New("quest stage step ranges must be contiguous and cover the plan")
			}
			for _, dependencyID := range stage.Dependencies {
				if stageIDs[dependencyID] == false {
					return errors.New("quest stage dependency must refer to an earlier stage")
				}
			}
			seenDependencies := make(map[string]bool, len(stage.Dependencies))
			for _, dependencyID := range stage.Dependencies {
				if seenDependencies[dependencyID] {
					return errors.New("quest stage has duplicate dependency")
				}
				seenDependencies[dependencyID] = true
			}
			if len(stage.Completion) == 0 {
				return errors.New("quest stage requires authoritative completion conditions")
			}
			for _, condition := range stage.Completion {
				if condition.Kind == "window_submitted" {
					return errors.New("quest stage completion cannot use local window submission")
				}
			}
			for _, condition := range stage.Preconditions {
				if err := condition.Validate(); err != nil {
					return err
				}
			}
			for _, condition := range stage.Completion {
				if err := condition.Validate(); err != nil {
					return err
				}
			}
			stageIDs[stage.ID] = true
			previousEnd = stage.EndStep
		}
		if previousEnd != len(p.Steps) {
			return errors.New("quest stage ranges must cover every plan step")
		}
		if !conditionsEqual(p.Stages[len(p.Stages)-1].Completion, p.Completion) {
			return errors.New("final quest stage completion must equal plan completion")
		}
	}
	ids := map[string]bool{}
	conditions := append(append([]Condition{}, p.Preconditions...), p.Completion...)
	for _, s := range p.Steps {
		if s.ID == "" || ids[s.ID] || s.Action.Skill == "" || s.TimeoutSeconds <= 0 || s.TimeoutSeconds > 3600 || len(s.Success) == 0 || s.MaximumCost < 0 {
			return errors.New("invalid executable step")
		}
		ids[s.ID] = true
		conditions = append(conditions, s.Preconditions...)
		conditions = append(conditions, s.Success...)
	}
	for _, c := range conditions {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p Plan) Complete(o Observation) bool {
	if !o.Connected || !o.Ready || o.CharacterID != p.CharacterID {
		return false
	}
	if p.Mode == "quest" {
		return len(p.Completion) > 0 && conditionsMatch(p.Completion, o)
	}
	matched := 0
	for _, t := range p.Targets {
		c := Condition{Kind: t.Kind + "_level", ID: t.ID, Value: int64(t.Level)}
		if c.Match(o) {
			matched++
		}
	}
	return len(p.Targets) > 0 && ((p.TargetPolicy == "any" && matched > 0) || (p.TargetPolicy == "all" && matched == len(p.Targets)))
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func taskLevelProblems(conditions []Condition, o Observation) []string {
	problems := make([]string, 0)
	for _, c := range conditions {
		if problem, failed := levelConditionProblem(c, o); failed {
			problems = append(problems, problem)
		}
	}
	return problems
}

func levelConditionProblem(c Condition, o Observation) (string, bool) {
	switch c.Kind {
	case "character_level":
		if c.Value < 1 || o.Character.Level < 1 || int64(o.Character.Level) < c.Value {
			return fmt.Sprintf("人物等级不满足：当前 %s，要求至少 %d", observedLevel(o.Character.Level), c.Value), true
		}
	case "pet_level":
		for _, pet := range o.Pets {
			if pet.ID != c.ID {
				continue
			}
			if c.Value < 1 || pet.Level < 1 || int64(pet.Level) < c.Value {
				return fmt.Sprintf("宠物 %s 等级不满足：当前 %s，要求至少 %d", c.ID, observedLevel(pet.Level), c.Value), true
			}
			return "", false
		}
		return fmt.Sprintf("宠物 %s 等级不满足：当前 未知，要求至少 %d", c.ID, c.Value), true
	}
	return "", false
}

func observedLevel(level int) string {
	if level < 1 {
		return fmt.Sprintf("未知(%d)", level)
	}
	return fmt.Sprintf("%d", level)
}

func taskLevelReason(problems []string) string {
	if len(problems) == 0 {
		return ""
	}
	return "任务等级条件不满足：" + strings.Join(problems, "；")
}

func (e *Engine) Preflight(ctx context.Context, p Plan) (Preflight, error) {
	if err := p.Validate(); err != nil {
		return Preflight{}, err
	}
	if e.Game == nil || e.Store == nil {
		return Preflight{}, errors.New("automation game and checkpoint store required")
	}
	o, err := e.Game.Observe(ctx)
	if err != nil {
		return Preflight{}, err
	}
	var validator SkillValidator
	if value, ok := e.Game.(SkillValidator); ok {
		validator = value
	}
	return EvaluatePreflight(ctx, p, o, validator)
}

// EvaluatePreflight evaluates a plan against an already-observed game state.
// It performs no observation, checkpoint, or game action, so callers that have
// a server-backed observation but cannot safely invoke Game.Observe can share
// the same readiness contract as Engine.Preflight. The optional validator is
// used to check that every deterministic step has an installed, read-only
// action validator. Execution still rechecks the live state at each write.
func EvaluatePreflight(ctx context.Context, p Plan, o Observation, validator SkillValidator) (Preflight, error) {
	if err := p.Validate(); err != nil {
		return Preflight{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Preflight{}, err
	}
	r := Preflight{Budget: p.Budget, Problems: []string{}, AlreadyComplete: p.Complete(o)}
	if !o.Connected || !o.Ready {
		r.Problems = append(r.Problems, "游戏状态尚未同步")
	}
	if o.CharacterID != p.CharacterID {
		r.Problems = append(r.Problems, "角色身份不一致")
	}
	if r.AlreadyComplete {
		r.Ready = len(r.Problems) == 0
		return r, nil
	}
	if !p.Budget.Known {
		r.Problems = append(r.Problems, "费用尚未验证")
	}
	if !o.UnlimitedFunds && (o.Gold < p.Budget.Reserve || o.Gold-p.Budget.Reserve < p.Budget.Minimum) {
		r.Problems = append(r.Problems, "可用石币不足最低启动资金与保留金额")
	}
	entry := p.EntryPreconditions(o)
	if !conditionsMatch(entry, o) {
		r.Problems = append(r.Problems, "任务前置条件不满足")
		r.Problems = append(r.Problems, preparationProblems(entry, o)...)
	}
	for _, t := range p.Targets {
		if t.Kind == "pet" {
			found := false
			for _, pet := range o.Pets {
				if pet.ID == t.ID {
					found = true
				}
			}
			if !found {
				r.Problems = append(r.Problems, "找不到目标宠物："+t.ID)
			}
		}
	}
	for _, s := range p.Steps {
		if !s.CostKnown {
			r.Problems = append(r.Problems, "步骤费用尚未验证："+s.ID)
		}
		if validator != nil {
			a := s.Action
			a.MaximumCost = s.MaximumCost
			if err := validator.ValidateSkill(ctx, a); err != nil {
				r.Problems = append(r.Problems, "步骤暂不可执行："+s.ID)
			}
		}
	}
	if len(r.Problems) == 0 {
		if departure, ok := validator.(DepartureValidator); ok {
			if action, present := p.departureAction(o); present {
				if err := departure.ValidateDeparture(ctx, action, o); err != nil {
					if ctx.Err() != nil {
						return Preflight{}, ctx.Err()
					}
					r.Problems = append(r.Problems, "出发路线暂不可用：请检查当前位置、人物等级及路线资料")
				}
			}
		}
	}
	r.Ready = len(r.Problems) == 0
	return r, nil
}

func (e *Engine) Start(ctx context.Context, p Plan) (Checkpoint, error) {
	pre, err := e.Preflight(ctx, p)
	if err != nil {
		return Checkpoint{}, err
	}
	if !pre.Ready {
		return Checkpoint{}, fmt.Errorf("preflight failed: %v", pre.Problems)
	}
	o, err := e.Game.Observe(ctx)
	if err != nil {
		return Checkpoint{}, err
	}
	if !o.Connected || !o.Ready || o.CharacterID != p.CharacterID {
		return Checkpoint{}, errors.New("game changed during preflight")
	}
	entry := p.EntryPreconditions(o)
	if problems := taskLevelProblems(entry, o); len(problems) > 0 {
		return Checkpoint{}, errors.New(taskLevelReason(problems))
	}
	if !p.Complete(o) && !conditionsMatch(entry, o) {
		return Checkpoint{}, errors.New("任务前置条件在预检后发生变化")
	}
	now := e.now()
	c := Checkpoint{Plan: p, Revision: 1, Status: Running, Phase: "ready", StartedAt: now, UpdatedAt: now, InitialSpent: o.Spent, WasDead: o.Dead}
	if len(p.Stages) > 0 {
		if p.Complete(o) {
			c.Stage = len(p.Stages)
			c.Step = len(p.Steps)
		} else {
			c.Stage = p.firstIncompleteStage(o)
			if c.Stage >= len(p.Stages) {
				return Checkpoint{}, errors.New("stage state is complete but root completion is unconfirmed")
			}
			c.Step = p.Stages[c.Stage].StartStep
		}
	}
	if p.Complete(o) {
		c.Status = Completed
		c.Confirmation = &o
		c.Reason = "已确认目标条件满足"
	}
	if err = e.Store.Create(ctx, c); err != nil {
		return Checkpoint{}, err
	}
	return c, nil
}

func (e *Engine) save(ctx context.Context, c *Checkpoint) error {
	old := c.Revision
	c.Revision++
	c.UpdatedAt = e.now()
	if err := e.Store.Save(ctx, *c, old); err != nil {
		c.Revision = old
		return err
	}
	return nil
}

func (e *Engine) pause(ctx context.Context, c *Checkpoint, reason string) error {
	c.Status = Paused
	c.Reason = reason
	return e.save(ctx, c)
}

// Tick performs at most one submission. The caller serializes ticks per
// character and wraps Game.Execute with aicontrol's ownership generation.
func (e *Engine) Tick(ctx context.Context, id string) (Checkpoint, error) {
	c, err := e.Store.Load(ctx, id)
	if err != nil {
		return c, err
	}
	if c.Status != Running {
		return c, nil
	}
	o, err := e.Game.Observe(ctx)
	if err != nil {
		return c, err
	}
	if len(c.Plan.Stages) > 0 {
		return e.tickStaged(ctx, &c, o)
	}
	if !o.Connected || !o.Ready || o.CharacterID != c.Plan.CharacterID {
		err = e.pause(ctx, &c, "连接中断或角色状态未同步")
		return c, err
	}
	if c.Plan.Complete(o) {
		c.Status = Completed
		c.Confirmation = &o
		// Completion may include locally observed window submission, which is
		// not a server acknowledgement. Keep the generic receipt accurate.
		c.Reason = "已确认目标条件满足"
		err = e.save(ctx, &c)
		return c, err
	}
	if e.now().Sub(c.StartedAt) >= time.Duration(c.Plan.MaximumSeconds)*time.Second {
		err = e.pause(ctx, &c, "达到时间上限")
		return c, err
	}
	if o.Dead && !c.WasDead {
		c.Deaths++
	}
	c.WasDead = o.Dead
	if c.Deaths > c.Plan.MaximumDeaths {
		err = e.pause(ctx, &c, "达到失败次数上限")
		return c, err
	}
	if o.SpendingKnown && o.Spent < c.InitialSpent {
		err = e.pause(ctx, &c, "消费账本失去连续性，需要重新核验预算")
		return c, err
	}
	// Reserve each step's verified maximum before submission. Legacy sessions
	// without an explicit expenditure stream can still enforce a conservative
	// budget; income never replenishes this authorization. If a server ledger
	// is available, larger observed costs also count against the limit.
	spent := c.ReservedSpend
	if o.SpendingKnown && o.Spent-c.InitialSpent > spent {
		spent = o.Spent - c.InitialSpent
	}
	if !o.UnlimitedFunds && (spent > c.Plan.Budget.MaximumSpend || o.Gold < c.Plan.Budget.Reserve) {
		err = e.pause(ctx, &c, "达到预算边界")
		return c, err
	}
	for _, t := range c.Plan.Targets {
		if t.Kind == "pet" {
			found := false
			for _, p := range o.Pets {
				if p.ID == t.ID {
					found = true
				}
			}
			if !found {
				err = e.pause(ctx, &c, "目标宠物已不在当前角色，等待重新确认")
				return c, err
			}
		}
	}
	if c.Step >= len(c.Plan.Steps) {
		if c.Plan.Mode == "leveling" {
			c.Step = 0
			c.Phase = "ready"
		} else {
			err = e.pause(ctx, &c, "步骤结束但服务端尚未确认任务完成")
			return c, err
		}
	}
	s := c.Plan.Steps[c.Step]
	if conditionsMatch(s.Success, o) {
		c.Step++
		c.Phase = "ready"
		c.StepStartedAt = time.Time{}
		err = e.save(ctx, &c)
		return c, err
	}
	if c.Phase == "prepared" {
		err = e.pause(ctx, &c, "上次操作提交结果未知，已停止重复执行")
		return c, err
	}
	if c.Phase == "submitted" {
		if e.now().Sub(c.StepStartedAt) >= time.Duration(s.TimeoutSeconds)*time.Second {
			err = e.pause(ctx, &c, "等待操作结果超时："+s.Description)
			return c, err
		}
		// Preserve death transitions even while waiting for an observation.
		err = e.save(ctx, &c)
		return c, err
	}
	if problems := taskLevelProblems(c.Plan.Preconditions, o); len(problems) > 0 {
		err = e.pause(ctx, &c, taskLevelReason(problems))
		return c, err
	}
	if !conditionsMatch(s.Preconditions, o) {
		err = e.pause(ctx, &c, "步骤前置条件已变化："+s.Description)
		return c, err
	}
	if !s.CostKnown || (!o.UnlimitedFunds && (s.MaximumCost > c.Plan.Budget.MaximumSpend-spent || o.Gold-c.Plan.Budget.Reserve < s.MaximumCost)) {
		err = e.pause(ctx, &c, "下一步超出已授权预算："+s.Description)
		return c, err
	}
	c.Phase = "prepared"
	if s.MaximumCost > math.MaxInt64-spent {
		c.ReservedSpend = math.MaxInt64
	} else {
		c.ReservedSpend = spent + s.MaximumCost
	}
	c.StepStartedAt = e.now()
	if err = e.save(ctx, &c); err != nil {
		return c, err
	}
	a := s.Action
	a.ExpectedRevision = o.Revision
	a.MaximumCost = s.MaximumCost
	if err = e.Game.Execute(ctx, a); err != nil {
		// Persist uncertainty even when takeover cancels the call context.
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		saveErr := e.pause(persist, &c, "操作结果需要核验："+s.Description)
		return c, errors.Join(err, saveErr)
	}
	c.Phase = "submitted"
	err = e.save(ctx, &c)
	return c, err
}

func validateStagedCheckpoint(c Checkpoint) error {
	if len(c.Plan.Stages) == 0 {
		return errors.New("staged checkpoint requires quest stages")
	}
	if c.Stage < 0 || c.Stage > len(c.Plan.Stages) {
		return errors.New("invalid quest stage checkpoint")
	}
	if !c.StageEntered {
		if c.Phase != "ready" {
			return errors.New("unentered quest stage has an in-flight phase")
		}
		if c.Stage == len(c.Plan.Stages) {
			if c.Step != len(c.Plan.Steps) {
				return errors.New("completed quest stages have an invalid step")
			}
			return nil
		}
		if c.Step != c.Plan.Stages[c.Stage].StartStep {
			return errors.New("unentered quest stage has an invalid step")
		}
		return nil
	}
	if c.Stage >= len(c.Plan.Stages) {
		return errors.New("completed quest checkpoint is marked entered")
	}
	stage := c.Plan.Stages[c.Stage]
	if c.Step < stage.StartStep || c.Step > stage.EndStep {
		return errors.New("quest checkpoint step is outside current stage")
	}
	switch c.Phase {
	case "ready":
		return nil
	case "prepared", "submitted":
		if c.Step >= stage.EndStep {
			return errors.New("in-flight quest step is outside current stage")
		}
		return nil
	default:
		return errors.New("invalid quest checkpoint phase")
	}
}

// tickStaged advances one flattened quest chain checkpoint. It deliberately
// keeps the staged state machine separate from the legacy single-task path:
// old plans retain their exact completion, timeout and retry behavior while a
// chain gets explicit stage entry and boundary handling.
func (e *Engine) tickStaged(ctx context.Context, c *Checkpoint, o Observation) (Checkpoint, error) {
	p := c.Plan
	if !o.Connected || !o.Ready || o.CharacterID != p.CharacterID {
		err := e.pause(ctx, c, "连接中断或角色状态未同步")
		return *c, err
	}
	if err := validateStagedCheckpoint(*c); err != nil {
		return *c, err
	}

	// One chain-wide timer, death counter and spending reservation are shared
	// by all stages. Run these fences before stage success/awaiting branches so
	// an over-budget or expired chain cannot advance merely because a late
	// observation happens to satisfy a predicate.
	if e.now().Sub(c.StartedAt) >= time.Duration(p.MaximumSeconds)*time.Second {
		err := e.pause(ctx, c, "达到时间上限")
		return *c, err
	}
	if o.Dead && !c.WasDead {
		c.Deaths++
	}
	c.WasDead = o.Dead
	if c.Deaths > p.MaximumDeaths {
		err := e.pause(ctx, c, "达到失败次数上限")
		return *c, err
	}
	if o.SpendingKnown && o.Spent < c.InitialSpent {
		err := e.pause(ctx, c, "消费账本失去连续性，需要重新核验预算")
		return *c, err
	}
	spent := c.ReservedSpend
	if o.SpendingKnown && o.Spent-c.InitialSpent > spent {
		spent = o.Spent - c.InitialSpent
	}
	if !o.UnlimitedFunds && (spent > p.Budget.MaximumSpend || o.Gold < p.Budget.Reserve) {
		err := e.pause(ctx, c, "达到预算边界")
		return *c, err
	}
	for _, target := range p.Targets {
		if target.Kind != "pet" {
			continue
		}
		found := false
		for _, pet := range o.Pets {
			if pet.ID == target.ID {
				found = true
				break
			}
		}
		if !found {
			err := e.pause(ctx, c, "目标宠物已不在当前角色，等待重新确认")
			return *c, err
		}
	}
	if c.Stage < len(p.Stages) {
		stage := p.Stages[c.Stage]
		if c.Step < stage.StartStep || c.Step > stage.EndStep {
			return *c, errors.New("quest checkpoint step is outside current stage")
		}

		if c.StageEntered && c.Step < stage.EndStep {
			step := p.Steps[c.Step]
			if conditionsMatch(step.Success, o) {
				c.Step++
				c.Phase = "ready"
				c.StepStartedAt = time.Time{}
				// Reaching EndStep never advances the stage in the same
				// checkpoint transition. The next observation must prove the
				// stage completion predicate.
				err := e.save(ctx, c)
				return *c, err
			}
		}
		// A stage's own completion is sufficient to reconcile a pending
		// operation. The root completion is intentionally not consulted here;
		// a descendant cannot skip a stage which this checkpoint has entered.
		// It is checked after a step success so the last-step transition always
		// persists EndStep before the stage can advance.
		if c.StageEntered && conditionsMatch(stage.Completion, o) {
			return e.finishStagedStage(ctx, c, o)
		}
		if c.StageEntered {
			dynamicLevels := p.stageEntryConditions(c.Stage)
			if problems := taskLevelProblems(dynamicLevels, o); len(problems) > 0 {
				err := e.pause(ctx, c, taskLevelReason(problems))
				return *c, err
			}
		}

		// prepared/submitted is an uncertain delivery fence. It is checked
		// before entry guards and the root completion so no later observation
		// can cause an unsafe replay.
		if c.StageEntered && c.Phase == "prepared" {
			err := e.pause(ctx, c, "上次操作提交结果未知，已停止重复执行")
			return *c, err
		}
		if c.StageEntered && c.Phase == "submitted" {
			if c.Step >= stage.EndStep {
				return *c, errors.New("submitted quest step is outside current stage")
			}
			step := p.Steps[c.Step]
			if e.now().Sub(c.StepStartedAt) >= time.Duration(step.TimeoutSeconds)*time.Second {
				err := e.pause(ctx, c, "等待操作结果超时："+step.Description)
				return *c, err
			}
			// Preserve the submitted phase and the death transition recorded
			// by the chain-wide fence above while waiting for success.
			err := e.save(ctx, c)
			return *c, err
		}
	}

	if c.Stage >= len(p.Stages) {
		if !c.StageEntered && p.Complete(o) {
			c.Status = Completed
			c.Confirmation = &o
			c.Reason = "已确认目标条件满足"
			err := e.save(ctx, c)
			return *c, err
		}
		err := e.pause(ctx, c, "阶段已结束但服务端尚未确认任务完成")
		return *c, err
	}

	if !c.StageEntered {
		// Skip only stages which are still unentered. A completed descendant
		// proves its reviewed ancestors, covering consumed prerequisite
		// rewards while preserving unrelated DAG branches.
		done := p.completedStageClosure(o)
		next := c.Stage
		for next < len(p.Stages) && done[next] {
			next++
		}
		if next != c.Stage {
			c.Stage = next
			c.StageEntered = false
			c.Phase = "ready"
			c.StepStartedAt = time.Time{}
			if next >= len(p.Stages) && p.Complete(o) {
				c.Step = len(p.Steps)
				c.Status = Completed
				c.Confirmation = &o
				c.Reason = "已确认目标条件满足"
			}
			if next < len(p.Stages) {
				c.Step = p.Stages[next].StartStep
			}
			err := e.save(ctx, c)
			return *c, err
		}
		stage := p.Stages[c.Stage]
		entry := p.stageEntryConditions(c.Stage)
		if !conditionsMatch(entry, o) {
			err := e.pause(ctx, c, "阶段前置条件已变化："+stage.Title)
			return *c, err
		}
		if problems := taskLevelProblems(entry, o); len(problems) > 0 {
			err := e.pause(ctx, c, taskLevelReason(problems))
			return *c, err
		}
		c.Step = stage.StartStep
	}

	stage := p.Stages[c.Stage]
	if c.Step >= stage.EndStep {
		// The final step succeeded in an earlier observation. Keep the stage
		// entered and wait for its independent authoritative completion proof.
		err := e.save(ctx, c)
		return *c, err
	}
	s := p.Steps[c.Step]
	if conditionsMatch(s.Success, o) {
		c.StageEntered = true
		c.Step++
		c.Phase = "ready"
		c.StepStartedAt = time.Time{}
		err := e.save(ctx, c)
		return *c, err
	}
	if !conditionsMatch(s.Preconditions, o) {
		err := e.pause(ctx, c, "步骤前置条件已变化："+s.Description)
		return *c, err
	}
	if !s.CostKnown || (!o.UnlimitedFunds && (s.MaximumCost > p.Budget.MaximumSpend-spent || o.Gold-p.Budget.Reserve < s.MaximumCost)) {
		err := e.pause(ctx, c, "下一步超出已授权预算："+s.Description)
		return *c, err
	}
	c.Phase = "prepared"
	c.StageEntered = true
	if s.MaximumCost > math.MaxInt64-spent {
		c.ReservedSpend = math.MaxInt64
	} else {
		c.ReservedSpend = spent + s.MaximumCost
	}
	c.StepStartedAt = e.now()
	if err := e.save(ctx, c); err != nil {
		return *c, err
	}
	a := s.Action
	a.ExpectedRevision = o.Revision
	a.MaximumCost = s.MaximumCost
	if err := e.Game.Execute(ctx, a); err != nil {
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		saveErr := e.pause(persist, c, "操作结果需要核验："+s.Description)
		return *c, errors.Join(err, saveErr)
	}
	c.Phase = "submitted"
	err := e.save(ctx, c)
	return *c, err
}

func (e *Engine) finishStagedStage(ctx context.Context, c *Checkpoint, o Observation) (Checkpoint, error) {
	if c.Stage < 0 || c.Stage >= len(c.Plan.Stages) {
		return *c, errors.New("invalid quest stage completion")
	}
	c.Stage++
	c.StageEntered = false
	c.Phase = "ready"
	c.StepStartedAt = time.Time{}
	if c.Stage < len(c.Plan.Stages) {
		c.Step = c.Plan.Stages[c.Stage].StartStep
		c.Status = Running
		c.Reason = ""
	} else {
		c.Step = len(c.Plan.Steps)
		if !c.Plan.Complete(o) {
			return *c, errors.New("final quest stage completed without root completion")
		}
		c.Status = Completed
		c.Confirmation = &o
		c.Reason = "已确认目标条件满足"
	}
	err := e.save(ctx, c)
	return *c, err
}

// Resume reconciles an interrupted step against current authoritative state.
// Unknown non-idempotent delivery never becomes a fresh attempt automatically.
func (e *Engine) Resume(ctx context.Context, id string) (Checkpoint, error) {
	c, err := e.Store.Load(ctx, id)
	if err != nil {
		return c, err
	}
	if c.Status != Paused {
		return c, errors.New("plan is not paused")
	}
	o, err := e.Game.Observe(ctx)
	if err != nil {
		return c, err
	}
	if !o.Connected || !o.Ready || o.CharacterID != c.Plan.CharacterID {
		return c, errors.New("game state is not synchronized")
	}
	if len(c.Plan.Stages) > 0 {
		return e.resumeStaged(ctx, &c, o)
	}
	if c.Plan.Complete(o) {
		c.Status = Completed
		c.Confirmation = &o
		c.Reason = "已确认目标条件满足"
		err = e.save(ctx, &c)
		return c, err
	}
	reconciled := false
	if (c.Phase == "prepared" || c.Phase == "submitted") && c.Step < len(c.Plan.Steps) {
		if !conditionsMatch(c.Plan.Steps[c.Step].Success, o) {
			return c, errors.New("上次操作结果尚未确认，不能重复提交")
		}
		c.Step++
		c.Phase = "ready"
		c.StepStartedAt = time.Time{}
		reconciled = true
	}
	if problems := taskLevelProblems(c.Plan.Preconditions, o); len(problems) > 0 {
		reason := taskLevelReason(problems)
		// A successful observation reconciles a prepared/submitted action even
		// when the task cannot be resumed yet. Persist that advancement while
		// keeping the checkpoint paused, so a later resume never replays it.
		if reconciled {
			c.Status = Paused
			c.Reason = reason
			if saveErr := e.save(ctx, &c); saveErr != nil {
				return c, errors.Join(errors.New(reason), saveErr)
			}
		}
		return c, errors.New(reason)
	}
	c.Status = Running
	c.Reason = ""
	err = e.save(ctx, &c)
	return c, err
}

func (e *Engine) resumeStaged(ctx context.Context, c *Checkpoint, o Observation) (Checkpoint, error) {
	p := c.Plan
	if err := validateStagedCheckpoint(*c); err != nil {
		return *c, err
	}
	if c.Stage >= len(p.Stages) {
		if p.Complete(o) {
			c.Status = Completed
			c.Confirmation = &o
			c.Reason = "已确认目标条件满足"
			err := e.save(ctx, c)
			return *c, err
		}
		return *c, errors.New("stage completion is not confirmed")
	}
	stage := p.Stages[c.Stage]
	if c.Step < stage.StartStep || c.Step > stage.EndStep {
		return *c, errors.New("quest checkpoint step is outside current stage")
	}

	if c.StageEntered {
		if conditionsMatch(stage.Completion, o) {
			result, err := e.finishStagedStage(ctx, c, o)
			if err != nil || result.Status == Completed || result.Stage >= len(p.Stages) {
				return result, err
			}
			// Resume must validate the next truly unentered stage's entry
			// guards before returning a running checkpoint. The helper also
			// applies completion closure so a descendant already completed in
			// this observation can skip another consumed ancestor.
			return e.resumeStagedEntry(ctx, c, o)
		}
		reconciled := false
		if c.Phase == "prepared" || c.Phase == "submitted" {
			if c.Step >= stage.EndStep {
				return *c, errors.New("上次操作结果尚未确认，不能重复提交")
			}
			if conditionsMatch(p.Steps[c.Step].Success, o) {
				c.Step++
				c.Phase = "ready"
				c.StepStartedAt = time.Time{}
				reconciled = true
			} else {
				dynamicLevels := p.stageEntryConditions(c.Stage)
				if problems := taskLevelProblems(dynamicLevels, o); len(problems) > 0 {
					reason := taskLevelReason(problems)
					c.Status = Paused
					c.Reason = reason
					if saveErr := e.save(ctx, c); saveErr != nil {
						return *c, errors.Join(errors.New(reason), saveErr)
					}
					return *c, errors.New(reason)
				}
				return *c, errors.New("上次操作结果尚未确认，不能重复提交")
			}
		}

		dynamicLevels := p.stageEntryConditions(c.Stage)
		if problems := taskLevelProblems(dynamicLevels, o); len(problems) > 0 {
			reason := taskLevelReason(problems)
			if reconciled {
				c.Status = Paused
				c.Reason = reason
				if saveErr := e.save(ctx, c); saveErr != nil {
					return *c, errors.Join(errors.New(reason), saveErr)
				}
			}
			return *c, errors.New(reason)
		}
		c.Status = Running
		c.Reason = ""
		err := e.save(ctx, c)
		return *c, err
	}

	return e.resumeStagedEntry(ctx, c, o)
}

func (e *Engine) resumeStagedEntry(ctx context.Context, c *Checkpoint, o Observation) (Checkpoint, error) {
	p := c.Plan
	done := p.completedStageClosure(o)
	next := c.Stage
	for next < len(p.Stages) && done[next] {
		next++
	}
	c.Stage = next
	c.StageEntered = false
	c.Phase = "ready"
	c.StepStartedAt = time.Time{}
	if next >= len(p.Stages) {
		if !p.Complete(o) {
			c.Status = Paused
			c.Reason = "阶段已结束但服务端尚未确认任务完成"
			if err := e.save(ctx, c); err != nil {
				return *c, errors.Join(errors.New(c.Reason), err)
			}
			return *c, errors.New(c.Reason)
		}
		c.Status = Completed
		c.Step = len(p.Steps)
		c.Confirmation = &o
		c.Reason = "已确认目标条件满足"
		err := e.save(ctx, c)
		return *c, err
	}
	c.Step = p.Stages[next].StartStep
	entry := p.stageEntryConditions(next)
	if !conditionsMatch(entry, o) {
		c.Status = Paused
		c.Reason = "阶段前置条件不满足：" + p.Stages[next].Title
		err := e.save(ctx, c)
		if err != nil {
			return *c, errors.Join(errors.New(c.Reason), err)
		}
		return *c, errors.New(c.Reason)
	}
	if problems := taskLevelProblems(entry, o); len(problems) > 0 {
		reason := taskLevelReason(problems)
		c.Status = Paused
		c.Reason = reason
		err := e.save(ctx, c)
		if err != nil {
			return *c, errors.Join(errors.New(reason), err)
		}
		return *c, errors.New(reason)
	}
	c.Status = Running
	c.Reason = ""
	err := e.save(ctx, c)
	return *c, err
}

func (e *Engine) Pause(ctx context.Context, id, reason string) (Checkpoint, error) {
	c, err := e.Store.Load(ctx, id)
	if err != nil {
		return c, err
	}
	if c.Status == Completed {
		return c, nil
	}
	err = e.pause(ctx, &c, reason)
	return c, err
}

func (e *Engine) Run(ctx context.Context, id string) error {
	interval := e.PollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		c, err := e.Tick(ctx, id)
		if err != nil {
			return err
		}
		if c.Status != Running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
