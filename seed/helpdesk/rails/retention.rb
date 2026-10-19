# frozen_string_literal: true

# Spec 0010 (AC-9): the retention step of `seed helpdesk`, run on the fresh
# path after the marker and on the already seeded path after the token
# check. It upserts one scheduler, noord-retention, that deletes the
# assistant's tickets (tag noord-assistant) N days after they end:
#
#   closed tickets  N days after their last close (last_close_at; close_at
#                   keeps the first close, so a ticket IT reopens and closes
#                   again would go at once)
#   merged tickets  N days after the merge (updated_at; Zammad never gives a
#                   merged ticket a close time)
#
# Seeded tickets never carry the tag, so Noord's other history is never
# touched. The marker leaves this file out, so editing it never forces a
# reset (internal/seed/helpdesk/marker.go).
#
# ENV NOORD_RETENTION_DAYS is TICKET_RETENTION_DAYS (1 to 365). It prints one
# line, NOORD_RESULT {"days":N,"matches":M}, where M is how many tickets the
# scheduler would delete now, or on failure "NOORD_FAILED <step>: <message>"
# to stderr and exits 1.

require 'json'

module NoordRetention
  module_function

  JOB_NAME = 'noord-retention'
  TAG = 'noord-assistant'
  WEEK = %w[Mon Tue Wed Thu Fri Sat Sun].freeze

  def run
    days = step('read days') { read_days }
    UserInfo.current_user_id = 1
    condition = step('build condition') { condition(days) }
    job = step('save scheduler') { save(condition) }
    matches = step('count matches') { count(job.condition) }
    finish(days: days, matches: matches)
  end

  def read_days
    days = Integer(ENV.fetch('NOORD_RETENTION_DAYS', ''), 10)
    raise ArgumentError, "NOORD_RETENTION_DAYS must be between 1 and 365, got #{days}" unless (1..365).cover?(days)

    days
  end

  def condition(days)
    {
      'operator'   => 'AND',
      'conditions' => [
        { 'name' => 'ticket.tags', 'operator' => 'contains one', 'value' => TAG },
        {
          'operator'   => 'OR',
          'conditions' => [
            ended('closed', 'ticket.last_close_at', days),
            ended('merged', 'ticket.updated_at', days),
          ],
        },
      ],
    }
  end

  # ended matches tickets in the named state whose time column is more than
  # days ago.
  def ended(state, column, days)
    ids = Ticket::State.where(name: state).pluck(:id).map(&:to_s)
    raise ArgumentError, "no ticket state named #{state}" if ids.empty?

    {
      'operator'   => 'AND',
      'conditions' => [
        { 'name' => 'ticket.state_id', 'operator' => 'is', 'value' => ids },
        { 'name' => column, 'operator' => 'before (relative)', 'value' => days.to_s, 'range' => 'day' },
      ],
    }
  end

  # save upserts the scheduler by name: a rerun with another window updates
  # the same job, never a second one. Zammad computes next_run_at on save.
  def save(condition)
    job = Job.find_or_initialize_by(name: JOB_NAME)
    job.assign_attributes(
      object:               'Ticket',
      condition:            condition,
      perform:              { 'ticket.action' => { 'value' => 'delete' } },
      timeplan:             timeplan,
      disable_notification: true,
      active:               true,
      note:                 'Spec 0010: deletes assistant tickets after TICKET_RETENTION_DAYS. Restored by make helpdesk-seed.',
      created_by_id:        job.created_by_id || 1,
      updated_by_id:        1
    )
    job.save!
    job
  end

  # timeplan is every day, every hour, at minute 0.
  def timeplan
    {
      'days'    => WEEK.index_with { true },
      'hours'   => (0..23).to_h { |h| [h.to_s, true] },
      'minutes' => { '0' => true },
    }
  end

  def count(condition)
    total, = Ticket.selectors(condition, limit: 1, access: 'ignore')
    total.to_i
  end

  def step(name)
    @step = name
    yield
  end

  def finish(result)
    $stdout.puts "NOORD_RESULT #{result.to_json}"
  end

  def fail!(error)
    warn "NOORD_FAILED #{@step || 'start'}: #{error.class}: #{error.message}"
    exit 1
  end
end

begin
  NoordRetention.run
rescue StandardError => e
  NoordRetention.fail!(e)
end
