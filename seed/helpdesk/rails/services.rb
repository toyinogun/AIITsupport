# frozen_string_literal: true

# Spec 0015 (AC-14): the services step of `seed helpdesk`, run right after
# bootstrap on the fresh and the already seeded path, before any REST call.
# Zammad's pods have no internet egress, and four of its services call out
# while serving a user's request: geo IP (the caller's IP to
# geo.zammad.com), images (email addresses to images.zammad.com), the
# holiday calendar, and geo location (addresses to OpenStreetMap). Blocked,
# each holds the request until its own timeout (journal 0076), so this step
# sets all four to none, the admin UI's "-" choice. That also keeps the
# helpdesk's personal data in the cluster. The marker leaves this file out,
# so editing it never forces a reset (internal/seed/helpdesk/marker.go).
#
# It prints one line, NOORD_RESULT {"disabled":[...]}, the settings it
# changed (empty when all four were already off), or on failure
# "NOORD_FAILED <step>: <message>" to stderr and exits 1.

require 'json'

module NoordServices
  module_function

  BACKENDS = %w[geo_ip_backend image_backend geo_calendar_backend geo_location_backend].freeze

  def run
    UserInfo.current_user_id = 1
    disabled = BACKENDS.select { |name| step("disable #{name}") { disable(name) } }
    finish(disabled: disabled)
  end

  # disable sets one backend to none and reports whether it was on.
  def disable(name)
    raise ArgumentError, "no setting named #{name}" unless Setting.exists?(name: name)
    return false if Setting.get(name).blank?

    Setting.set(name, '')
    true
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
  NoordServices.run
rescue StandardError => e
  NoordServices.fail!(e)
end
