# frozen_string_literal: true

# Spec 0006: a read only snapshot of the seeded helpdesk, for the TEST_ZAMMAD
# live tests (and for you, to see what a reset produced). It writes nothing.
# Prints one line: NOORD_RESULT {json}. Times are RFC 3339 UTC.

require 'json'

module NoordReport
  module_function

  def run
    org = Organization.find_by(name: 'Noord Logistics')
    $stdout.puts "NOORD_RESULT #{{
      marker: org&.note,
      org_members: org ? User.where(organization_id: org.id).count : 0,
      ldap_users: User.where('source LIKE ?', 'Ldap::%').count,
      ruben_roles: User.find_by(login: 'ruben.mulder')&.roles&.map(&:name)&.sort,
      tickets: Ticket.order(:id).map { |t| ticket(t) },
      answers: KnowledgeBase::Answer.order(:id).map { |a| answer(a) },
      categories: KnowledgeBase::Category.order(:id).map { |c| { title: c.translations.first&.title, icon: c.category_icon } }
    }.to_json}"
  end

  def ticket(t)
    {
      tags: t.tag_list, title: t.title, state: t.state.name, owner: t.owner&.login, customer: t.customer.login,
      group: t.group.name, created_at: t.created_at.utc.iso8601, close_at: t.close_at&.utc&.iso8601,
      articles: t.articles.order(:id).map do |a|
        { from: a.created_by.login, sender: a.sender.name, type: a.type.name, internal: a.internal, at: a.created_at.utc.iso8601 }
      end
    }
  end

  def answer(a)
    { title: a.translations.first&.title, category: a.category.translations.first&.title,
      created_at: a.created_at.utc.iso8601, updated_at: a.updated_at.utc.iso8601,
      published: a.published_at.present? && a.archived_at.nil? && a.internal_at.nil? }
  end
end

NoordReport.run
