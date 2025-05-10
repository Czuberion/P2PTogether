#include "role_store.h"
#include <QDebug>
#include <QReadLocker>
#include <QWriteLocker>

namespace P2P {
namespace Roles {

RoleStore::RoleStore(QObject* parent) : QObject(parent) {
    qRegisterMetaType<client::p2p::RoleDefinitionsUpdate>(
        "client::p2p::RoleDefinitionsUpdate");
    qRegisterMetaType<client::p2p::PeerRoleAssignment>(
        "client::p2p::PeerRoleAssignment");
    qRegisterMetaType<client::p2p::AllPeerRoleAssignments>(
        "client::p2p::AllPeerRoleAssignments");
    // qRegisterMetaType<client::ServerMsg>("client::ServerMsg"); // Already
    // done by ControlStreamWorker probably
}

RoleStore::~RoleStore() {}

void RoleStore::processRoleDefinitionsUpdate(
    const client::p2p::RoleDefinitionsUpdate& update) {
    QWriteLocker locker(&storeLock);
    roleDefinitions.clear();
    for (const auto& defProto : update.definitions()) {
        if (defProto.name().empty()) {
            qWarning() << "RoleStore: Received role definition with empty "
                          "name. Skipping.";
            continue;
        }
        // Normalize role name for consistent lookup, e.g., toLower()
        // For now, assuming names from server are canonical.
        roleDefinitions.insert(QString::fromStdString(defProto.name()),
                               defProto);
    }
    locker.unlock(); // Unlock before emitting signal
    qInfo() << "RoleStore: Processed RoleDefinitionsUpdate. Total definitions:"
            << roleDefinitions.size();
    emit definitionsChanged();
    emit allAssignmentsRefreshed(); // Definitions changing can affect all
                                    // permissions
}

void RoleStore::processPeerRoleAssignment(
    const client::p2p::PeerRoleAssignment& assignment) {
    QString peerId = QString::fromStdString(assignment.peer_id());
    if (peerId.isEmpty()) {
        qWarning() << "RoleStore: Received PeerRoleAssignment with empty "
                      "peer_id. Skipping.";
        return;
    }

    bool changed = false;
    QWriteLocker locker(&storeLock);

    // HLC Check: Only update if incoming is newer or if no current assignment
    // exists
    auto it = peerAssignments.find(peerId);
    if (it == peerAssignments.end() ||
        assignment.hlc_ts() > it.value().hlc_ts()) {
        if (assignment.assigned_role_names_size() > 0) {
            peerAssignments.insert(peerId, assignment);
        } else {
            // If new assignment has no roles, remove the peer's assignment
            // entry
            if (it != peerAssignments.end()) {
                peerAssignments.erase(it);
            }
        }
        changed = true;
        qInfo() << "RoleStore: Processed PeerRoleAssignment for peer" << peerId
                << "New HLC:" << assignment.hlc_ts();
    } else {
        qInfo() << "RoleStore: Skipped stale PeerRoleAssignment for peer"
                << peerId << "Incoming HLC:" << assignment.hlc_ts()
                << "Existing HLC:"
                << (it != peerAssignments.end() ? it.value().hlc_ts() : -1);
    }

    locker.unlock(); // Unlock before emitting signal
    if (changed) {
        emit peerRolesChanged(peerId);
    }
}

void RoleStore::processAllPeerAssignments(
    const client::p2p::AllPeerRoleAssignments& allAssignments) {
    // This is a full snapshot. Be careful about HLC.
    // Option 1: Clear all and replace, trusting the snapshot's HLC.
    // Option 2: Iterate and call processPeerRoleAssignment for each, respecting
    // individual HLCs. Option 2 is generally safer against overwriting newer
    // individual updates with an older snapshot.

    // TODO: Consider HLC for the snapshot message itself
    // (allAssignments.hlc_ts())
    //       to decide if this whole snapshot should even be processed if it's
    //       older than the last time a full snapshot was applied. For now,
    //       process individual items.

    qInfo() << "RoleStore: Processing AllPeerAssignments snapshot. HLC:"
            << allAssignments.hlc_ts()
            << "Items:" << allAssignments.assignments_size();

    // To avoid emitting peerRolesChanged for every single peer if many change,
    // we can lock, do all updates, then emit one "allAssignmentsRefreshed"
    // signal. However, processPeerRoleAssignment already emits
    // peerRolesChanged. For simplicity now, let's just call
    // processPeerRoleAssignment for each. A more optimized version might batch
    // UI updates.

    bool anyChange = false;
    for (const auto& assignmentProto : allAssignments.assignments()) {
        QString peerId = QString::fromStdString(assignmentProto.peer_id());
        if (peerId.isEmpty())
            continue;

        // Temporarily unlock for individual processing, then re-lock if needed,
        // or handle locking inside. For now, let processPeerRoleAssignment
        // handle its own locking. This means multiple lock/unlock cycles.
        processPeerRoleAssignment(
            assignmentProto); // This will emit individual signals
        // To track if *any* change happened for the allAssignmentsRefreshed
        // signal: We'd need processPeerRoleAssignment to return a bool.
    }
    qInfo() << "RoleStore: Finished processing AllPeerAssignments snapshot.";
    // After all individual assignments are processed:
    emit
    allAssignmentsRefreshed(); // Signal that a bulk update may have occurred
}

QMap<QString, client::p2p::RoleDefinition>
RoleStore::getRoleDefinitions() const {
    QReadLocker locker(&storeLock);
    return roleDefinitions; // Returns a copy
}

client::p2p::RoleDefinition
RoleStore::getRoleDefinition(const QString& roleName, bool* ok) const {
    QReadLocker locker(&storeLock);
    // Consider normalizing roleName (e.g., toLower()) if keys are stored
    // normalized
    if (roleDefinitions.contains(roleName)) {
        if (ok)
            *ok = true;
        return roleDefinitions.value(roleName); // Returns a copy
    }
    if (ok)
        *ok = false;
    return client::p2p::RoleDefinition(); // Return default/empty if not found
}

QVector<QString> RoleStore::getAssignedRoleNames(const QString& peerId) const {
    QReadLocker locker(&storeLock);
    auto it = peerAssignments.find(peerId);
    if (it != peerAssignments.end()) {
        const auto& assignmentProto = it.value();
        QVector<QString> names;
        names.reserve(assignmentProto.assigned_role_names_size());
        for (const std::string& roleNameStd :
             assignmentProto.assigned_role_names()) {
            names.append(QString::fromStdString(roleNameStd));
        }
        return names;
    }
    return QVector<QString>();
}

qint64 RoleStore::getPeerAssignmentHlc(const QString& peerId) const {
    QReadLocker locker(&storeLock);
    auto it = peerAssignments.find(peerId);
    if (it != peerAssignments.end()) {
        return it.value().hlc_ts();
    }
    return -1; // Or some other indicator of not found/no HLC
}

quint32 RoleStore::getPermissionsForPeer(const QString& peerId) const {
    quint32 finalPermissions = 0;
    QReadLocker locker(&storeLock); // Lock for the duration of calculation

    auto it = peerAssignments.find(peerId);
    if (it == peerAssignments.end()) {
        return 0; // No roles assigned, so no permissions
    }

    const auto& assignmentProto = it.value();
    for (const std::string& roleNameStd :
         assignmentProto.assigned_role_names()) {
        QString roleName = QString::fromStdString(roleNameStd);
        // Consider normalizing roleName if keys in roleDefinitions are
        // normalized
        if (roleDefinitions.contains(roleName)) {
            finalPermissions |= roleDefinitions.value(roleName).permissions();
        } else {
            qWarning() << "RoleStore: Peer" << peerId
                       << "is assigned unknown role:" << roleName;
        }
    }
    return finalPermissions;
}

} // namespace Roles
} // namespace P2P
