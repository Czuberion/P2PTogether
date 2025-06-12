/*!
 * \file mpvwidget.h
 * \brief Qt/OpenGL widget for libmpv (adapted from mpv-examples).
 *
 * \details
 * Original source:
 * [mpv-player/mpv-examples/libmpv/qt_opengl/mpvwidget.h](https://github.com/mpv-player/mpv-examples/blob/master/libmpv/qt_opengl/mpvwidget.h)
 *
 * \note
 * This file is public domain per the example-repo’s broad grant.
 *
 * \see player/mpvwidget.cpp
 * \see player/qthelper.hpp
 */
#pragma once

#include "qthelper.hpp"
#include <QtOpenGLWidgets/QOpenGLWidget>
#include <mpv/client.h>
#include <mpv/render_gl.h>

namespace player {

class MpvWidget Q_DECL_FINAL : public QOpenGLWidget {
    Q_OBJECT
public:
    MpvWidget(QWidget* parent = nullptr, Qt::WindowFlags f = Qt::WindowFlags());
    ~MpvWidget();
    void command(const QVariant& params);
    void setProperty(const QString& name, const QVariant& value);
    QVariant getProperty(const QString& name) const;
    QSize sizeHint() const override { return {}; }
Q_SIGNALS:
    void durationChanged(double value);
    void positionChanged(double value);
    void actualFileEnded();
    void fileLoaded();

protected:
    void initializeGL() Q_DECL_OVERRIDE;
    void paintGL() Q_DECL_OVERRIDE;
private Q_SLOTS:
    void on_mpv_events();
    void maybeUpdate();

private:
    void handle_mpv_event(mpv_event* event);
    static void on_update(void* ctx);

    mpv_handle* mpv;
    mpv_render_context* mpv_gl;
};

} // namespace player
