# frozen_string_literal: true

# Spec 0006: the first Rails step of `seed helpdesk`. It sets up what Zammad's
# REST API cannot: the setup wizard, settings, the seed admin, the CA, the
# IT Support group, the assistant role, user, and token, the empty knowledge
# base, and the LDAP source. Then it runs the LDAP import inline and puts
# every synced user in the Noord Logistics organization.
#
# It decides the helpdesk state before any write and prints one line:
#   NOORD_RESULT {"state":"fresh"|"already_seeded"|"needs_reset", ...}
# On fresh it adds the ids the REST fill needs and a login to id map of every
# LDAP user. Secrets arrive only as environment variables, never as
# arguments. On failure it prints "NOORD_FAILED <step>: <message>" to stderr
# and exits 1.

require 'json'

module NoordBootstrap
  module_function

  ORG_NAME     = 'Noord Logistics'
  CA_PATH      = '/opt/noord-seed/ca.pem'
  BASE_DN      = 'DC=ad,DC=noord,DC=example'
  BIND_DN      = "CN=svc-zammad,OU=Service Accounts,OU=Noord,#{BASE_DN}".freeze
  # Zammad searches users and groups under one base (journal 0019), so it is
  # OU=Noord and the filter keeps only people with a department.
  SEARCH_BASE  = "OU=Noord,#{BASE_DN}".freeze
  USER_FILTER  = '(&(objectCategory=person)(objectClass=user)(department=*))'
  AGENTS_GROUP = "CN=APP-Helpdesk-Agents,OU=Groups,OU=Noord,#{BASE_DN}".freeze
  ROLE_NAME    = 'Noord Assistant'
  ASSISTANT    = 'noord-assistant'
  PERMISSIONS  = %w[ticket.agent knowledge_base.reader].freeze
  KB_TITLE     = 'Noord IT Help'
  KB_LOCALE    = 'en-us'

  def run
    marker = env!('NOORD_MARKER')
    admin_email = env!('NOORD_ADMIN_EMAIL')

    state = step('check state') { decide(marker, admin_email) }
    return finish(state: state) if state != 'fresh'

    UserInfo.current_user_id = 1
    step('settings') { settings(env!('NOORD_FQDN'), env!('NOORD_HTTP_TYPE')) }
    step('mail off') { mail_off }
    group = step('group') { it_support_group(env!('NOORD_GROUP')) }
    step('stock content') { remove_stock_content }
    admin = step('seed admin') { seed_admin(admin_email, env!('NOORD_ADMIN_PASSWORD'), group) }
    step('ca') { trust_ca }
    assistant = step('assistant') { assistant_user(group, env!('NOORD_ASSISTANT_TOKEN')) }
    kb = step('knowledge base') { knowledge_base }
    step('ldap source') { ldap_source(env!('NOORD_LDAP_HOST'), env!('NOORD_BIND_PASSWORD')) }
    stats = step('ldap import') { ldap_import }
    org = step('organization') { organization }

    finish(
      state: 'fresh', group_id: group.id, admin_id: admin.id, assistant_id: assistant.id,
      kb_id: kb.id, kb_locale_id: kb.kb_locales.first.id, organization_id: org.id,
      roles: Role.pluck(:name, :id).to_h, import: stats, users: ldap_users
    )
  end

  # already_seeded: the organization note holds the expected marker.
  # fresh: the setup wizard is not done and neither the seed admin nor the
  #   organization exists.
  # needs_reset: anything else, including a run that stopped halfway.
  def decide(marker, admin_email)
    org = Organization.find_by(name: ORG_NAME)
    return 'already_seeded' if org && org.note == marker
    return 'fresh' if !Setting.get('system_init_done') && org.nil? && User.find_by(email: admin_email).nil?

    'needs_reset'
  end

  # fqdn and http_type are where Zammad's links point: the local stack's
  # 127.0.0.1:58080 over http, or the cluster's tailnet host over https
  # (spec 0015). cmd/seed derives both from ZAMMAD_PUBLIC_URL.
  def settings(fqdn, http_type)
    Setting.set('fqdn', fqdn)
    Setting.set('http_type', http_type)
    Setting.set('locale_default', KB_LOCALE)
    Setting.set('timezone_default', 'Europe/Amsterdam')
    # AC-17: no self signup and no password reset, so AD is the only way in.
    Setting.set('user_create_account', false)
    Setting.set('user_lost_password', false)
    Setting.set('organization', ORG_NAME)
    Setting.set('kb_active', true)
    Setting.set('kb_active_publicly', true)
    Setting.set('system_init_done', true)
  end

  # Both notification channels off and no email account, so no mail leaves.
  def mail_off
    Channel.where(area: 'Email::Notification').find_each { |channel| channel.update!(active: false) }
    Channel.where(area: 'Email::Account').destroy_all
  end

  def it_support_group(name)
    Group.create!(name: name, active: true)
  end

  # Zammad's welcome ticket, demo customer, demo organization, and Users
  # group, in that order (each depends on the one before it being gone).
  def remove_stock_content
    Ticket.destroy_all
    User.where(login: 'nicole.braun@zammad.org').destroy_all
    Organization.where(name: 'Zammad Foundation').destroy_all
    Group.where(name: 'Users').destroy_all
  end

  # The Agent role gets full access to IT Support, so Ruben (Agent through
  # the LDAP role map) and the seed admin can work its tickets.
  def seed_admin(email, password, group)
    agent = Role.find_by!(name: 'Agent')
    agent.group_names_access_map = { group.name => ['full'] }
    agent.save!

    User.create!(login: email, email: email, firstname: 'Seed', lastname: 'Admin',
                 password: password, active: true,
                 roles: Role.where(name: %w[Admin Agent]))
  end

  def trust_ca
    SSLCertificate.create!(certificate: File.read(CA_PATH))
  end

  def assistant_user(group, token_value)
    role = Role.create!(name: ROLE_NAME, active: true, default_at_signup: false,
                        permissions: Permission.where(name: PERMISSIONS))
    role.group_names_access_map = { group.name => ['full'] }
    role.save!

    user = User.create!(login: ASSISTANT, firstname: 'Noord IT', lastname: 'Assistant',
                        email: "#{ASSISTANT}@noord.example", active: true, roles: [role])
    token = Token.create!(action: 'api', persistent: true, name: ASSISTANT, user_id: user.id,
                          preferences: { permission: PERMISSIONS })
    # Token makes its own value on create; install ZAMMAD_TOKEN instead.
    token.update_columns(token: token_value) # rubocop:disable Rails/SkipsModelValidations
    user
  end

  # REST cannot set the KB locale, so the empty KB is made here. Zammad adds
  # the primary locale's translation itself; we only title it (journal 0021).
  def knowledge_base
    locale = ::Locale.find_by!(locale: KB_LOCALE)
    kb = KnowledgeBase.create!(
      iconset: 'FontAwesome', color_highlight: '#38ae6a', color_header: '#f9fafb',
      color_header_link: 'hsl(206,8%,50%)', homepage_layout: 'grid', category_layout: 'grid',
      active: true, kb_locales_attributes: [{ system_locale_id: locale.id, primary: true }]
    )
    translation = kb.translations.find_or_initialize_by(kb_locale: kb.kb_locales.first)
    translation.update!(title: KB_TITLE)
    kb
  end

  # host is samba in Compose, the Samba Service FQDN in the cluster; the
  # certificate is checked against the CA trusted above either way.
  def ldap_source(host, bind_password)
    agent = Role.find_by!(name: 'Agent')
    admin = Role.find_by!(name: 'Admin')
    LdapSource.create!(
      name: 'Noord AD', active: true, prio: 1,
      preferences: {
        host: host, port: 636, ssl: 'ssl', ssl_verify: true,
        bind_user: BIND_DN, bind_pw: bind_password, base_dn: SEARCH_BASE,
        user_filter: USER_FILTER, user_uid: 'samaccountname',
        user_attributes: {
          'samaccountname' => 'login', 'givenname' => 'firstname', 'sn' => 'lastname',
          'mail' => 'email', 'department' => 'department'
        },
        group_filter: '(objectClass=group)', group_uid: 'dn',
        group_role_map: { AGENTS_GROUP.downcase => [agent.id.to_s, admin.id.to_s] },
        unassigned_users: 'sigup_roles'
      }
    )
    Setting.set('ldap_integration', true)
  end

  # Runs the import here, not in the scheduler, so a failure stops the seed.
  # While this job is unfinished the scheduler queues no second one.
  def ldap_import
    job = ImportJob.create!(name: 'Import::Ldap')
    job.start
    job.reload
    result = job.result || {}
    raise "import error: #{result[:error] || result['error']}" if result[:error] || result['error']

    failed = failed_count(result)
    raise "#{failed} users failed to import" if failed.positive?
    raise 'the import finished without a result' if job.finished_at.nil?

    result
  end

  def failed_count(result)
    result.sum do |key, value|
      if value.is_a?(Hash)
        failed_count(value)
      else
        key.to_s == 'failed' ? value.to_i : 0
      end
    end
  end

  # Every user the import made joins Noord Logistics, leavers included.
  def organization
    org = Organization.create!(name: ORG_NAME, shared: false, domain_assignment: false, active: true)
    User.where('source LIKE ?', 'Ldap::%').find_each { |user| user.update!(organization_id: org.id) }
    org
  end

  def ldap_users
    User.where('source LIKE ?', 'Ldap::%').includes(:roles, :organization).to_h do |user|
      [user.login, { id: user.id, active: user.active, organization: user.organization&.name,
                     roles: user.roles.map(&:name).sort }]
    end
  end

  def env!(name)
    value = ENV.fetch(name, '')
    raise ArgumentError, "#{name} is not set" if value.empty?

    value
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
  NoordBootstrap.run
rescue StandardError => e
  NoordBootstrap.fail!(e)
end
