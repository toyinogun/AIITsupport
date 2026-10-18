# frozen_string_literal: true

# Spec 0006: runs one more LDAP import inline, as Zammad's scheduler would,
# for the TEST_ZAMMAD live test that checks a later sync leaves the seeded
# helpdesk alone. Prints NOORD_RESULT {"import":{...stats}}, or on failure
# "NOORD_FAILED ldap sync: <message>" to stderr and exits 1.

require 'json'

begin
  job = ImportJob.create!(name: 'Import::Ldap')
  job.start
  job.reload
  raise "import error: #{job.result[:error] || job.result['error']}" if job.result[:error] || job.result['error']

  $stdout.puts "NOORD_RESULT #{{ import: job.result }.to_json}"
rescue StandardError => e
  warn "NOORD_FAILED ldap sync: #{e.class}: #{e.message}"
  exit 1
end
