# frozen_string_literal: true

# Spec 0006: the last Rails step of `seed helpdesk`. The REST fill stamps
# everything with the seed time; this step writes the dates from the seed
# files instead, rebuilds the search index, and writes the marker last.
#
# stdin is JSON from cmd/seed: the marker, then every ticket, article, and
# KB answer id with its dates (RFC 3339 UTC, already converted from
# Europe/Amsterdam). History rows keep the seed time (accepted in the spec).
# It prints one line, NOORD_RESULT {"state":"seeded",...}, or on failure
# "NOORD_FAILED <step>: <message>" to stderr and exits 1.

require 'json'

module NoordBackdate
  module_function

  ORG_NAME = 'Noord Logistics'

  # Zammad's escalation and time to close columns, cleared on every ticket.
  ESCALATION_COLUMNS = %i[
    escalation_at first_response_escalation_at update_escalation_at close_escalation_at
    first_response_in_min first_response_diff_in_min update_in_min update_diff_in_min
    close_in_min close_diff_in_min
  ].freeze

  def run
    payload = step('read input') { JSON.parse($stdin.read) }
    marker = step('check marker') { check_marker(payload['marker']) }

    step('backdate') do
      ActiveRecord::Base.transaction do
        payload.fetch('tickets').each { |t| backdate_ticket(t) }
        payload.fetch('answers').each { |a| backdate_answer(a) }
      end
    end
    step('search index') do
      Ticket.search_index_reload(silent: true)
      KnowledgeBase::Answer::Translation.search_index_reload(silent: true)
    end
    step('marker') { Organization.find_by!(name: ORG_NAME).update!(note: marker) }

    finish(state: 'seeded', tickets: payload['tickets'].size, answers: payload['answers'].size)
  end

  # The marker on stdin must be the one cmd/seed passed in the environment.
  def check_marker(marker)
    raise 'the marker on stdin does not match NOORD_MARKER' if marker.blank? || marker != ENV.fetch('NOORD_MARKER', '')

    marker
  end

  # rubocop:disable Rails/SkipsModelValidations
  def backdate_ticket(data)
    @id = "ticket #{data['id']} (#{data['key']})"
    ticket = Ticket.find(data['id'])
    close_at = time(data['close_at'])
    ticket.update_columns(
      created_at: time(data['created_at']), updated_at: time(data['updated_at']),
      close_at: close_at, last_close_at: close_at,
      first_response_at: time(data['first_response_at']),
      last_contact_at: time(data['last_contact_at']),
      last_contact_customer_at: time(data['last_contact_customer_at']),
      last_contact_agent_at: time(data['last_contact_agent_at']),
      **ESCALATION_COLUMNS.index_with(nil)
    )
    data.fetch('articles').each do |a|
      @id = "article #{a['id']} of ticket #{data['id']}"
      at = time(a['at'])
      article = Ticket::Article.find_by!(id: a['id'], ticket_id: ticket.id)
      article.update_columns(created_at: at, updated_at: at)
    end
  end

  def backdate_answer(data)
    @id = "answer #{data['id']} (#{data['slug']})"
    created = time(data['created_at'])
    updated = time(data['updated_at'])
    answer = KnowledgeBase::Answer.find(data['id'])
    answer.update_columns(created_at: created, updated_at: updated, published_at: created)
    answer.translations.each do |translation|
      translation.update_columns(created_at: created, updated_at: updated, edited_at: updated)
    end
  end
  # rubocop:enable Rails/SkipsModelValidations

  def time(value)
    value && Time.iso8601(value)
  end

  def step(name)
    @step = name
    @id = nil
    yield
  end

  def finish(result)
    $stdout.puts "NOORD_RESULT #{result.to_json}"
  end

  def fail!(error)
    where = @id ? "#{@step} (#{@id})" : (@step || 'start')
    warn "NOORD_FAILED #{where}: #{error.class}: #{error.message}"
    exit 1
  end
end

begin
  NoordBackdate.run
rescue StandardError => e
  NoordBackdate.fail!(e)
end
